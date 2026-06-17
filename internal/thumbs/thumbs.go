// Package thumbs implements an on-disk LRU thumbnail cache.
//
// Thumbnails are generated on demand from a caller-supplied Generator,
// resized to a configured height (preserving aspect ratio) using
// CatmullRom resampling, and encoded as JPEG quality 85. The cache is
// concurrency-safe: concurrent Get calls for the same key only run the
// Generator once. Eviction is mtime-based (oldest first) and triggered
// asynchronously after writes; callers may also invoke EvictIfNeeded
// directly.
package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
)

// jpegQuality is the encoding quality for cached thumbnails. This is
// independent of the user's configured save quality.
const jpegQuality = 85

// Options configures a Cache.
type Options struct {
	// Dir is the cache directory. It will be created with mode 0o755 if
	// it does not exist.
	Dir string
	// HeightPx is the target thumbnail height in pixels; width preserves
	// the source aspect ratio.
	HeightPx int
	// MaxBytes is a soft cap on total bytes on disk. Eviction triggers
	// when the total exceeds this value.
	MaxBytes int64
	// SourceID identifies the configured source (e.g. "local:/data" or
	// "s3:bucket/prefix") and is included in the cache key so different
	// deployments do not collide on the same Dir.
	SourceID string
}

// Generator produces an original image to be thumbnailed.
type Generator func(ctx context.Context) (image.Image, error)

// Cache is an on-disk LRU thumbnail cache. It is safe for concurrent
// use by multiple goroutines.
type Cache struct {
	opts Options
	sf   *singleflight
}

// New creates a Cache with the given options. Dir is created with mode
// 0o755 if it does not exist; an error is returned if Dir exists but
// is not a directory or any option is invalid.
func New(opts Options) (*Cache, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	if err := ensureDir(opts.Dir); err != nil {
		return nil, err
	}
	return &Cache{
		opts: opts,
		sf:   &singleflight{calls: make(map[string]*sfCall)},
	}, nil
}

// validateOptions checks Options invariants.
func validateOptions(opts Options) error {
	if opts.Dir == "" {
		return fmt.Errorf("invalid options: Dir is empty")
	}
	if opts.HeightPx <= 0 {
		return fmt.Errorf("invalid options: HeightPx must be > 0")
	}
	if opts.MaxBytes <= 0 {
		return fmt.Errorf("invalid options: MaxBytes must be > 0")
	}
	if opts.SourceID == "" {
		return fmt.Errorf("invalid options: SourceID is empty")
	}
	return nil
}

// ensureDir creates dir if missing, or verifies it is a directory.
func ensureDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat cache dir: %w", err)
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return fmt.Errorf("create cache dir: %w", mkErr)
		}
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("cache dir %q exists and is not a directory", dir)
	}
	return nil
}

// hashKey returns the on-disk filename (without extension) for a cache
// entry identified by source key and etag.
func (c *Cache) hashKey(key, etag string) string {
	h := sha256.Sum256([]byte(c.opts.SourceID + "|" + key + "|" + etag))
	return hex.EncodeToString(h[:])
}

// pathFor returns the absolute on-disk path for a cache entry.
func (c *Cache) pathFor(key, etag string) string {
	return filepath.Join(c.opts.Dir, c.hashKey(key, etag)+".jpg")
}

// Get returns the cached thumbnail bytes (JPEG) for the given source
// key + etag. On a hit, the on-disk file's mtime is bumped to track LRU
// order. On a miss, generate is invoked to produce the original image,
// the thumbnail is generated, written atomically, and returned.
//
// Concurrent Get calls for the same key+etag will only invoke generate
// once; the other callers receive the same result.
func (c *Cache) Get(ctx context.Context, key, etag string, generate Generator) ([]byte, error) {
	hashed := c.hashKey(key, etag)
	path := filepath.Join(c.opts.Dir, hashed+".jpg")

	if data, ok := readIfExists(path); ok {
		// Best-effort mtime bump; ignore errors.
		_ = os.Chtimes(path, time.Now(), time.Now())
		return data, nil
	}

	return c.sf.Do(hashed, func() ([]byte, error) {
		// Re-check after acquiring the singleflight slot in case a
		// concurrent Get already produced the file.
		if data, ok := readIfExists(path); ok {
			_ = os.Chtimes(path, time.Now(), time.Now())
			return data, nil
		}
		return c.generateAndStore(ctx, path, generate)
	})
}

// readIfExists returns the file contents if path exists, ok=false otherwise.
func readIfExists(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// generateAndStore runs the Generator, encodes the thumbnail, writes it
// atomically, schedules an async eviction, and returns the bytes.
func (c *Cache) generateAndStore(ctx context.Context, path string, generate Generator) ([]byte, error) {
	src, err := generate(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate source image: %w", err)
	}
	if src == nil {
		return nil, fmt.Errorf("generate source image: nil image")
	}
	thumb := resizeToHeight(src, c.opts.HeightPx)
	data, err := encodeJPEG(thumb)
	if err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	if err := writeAtomic(path, data); err != nil {
		return nil, fmt.Errorf("write thumbnail: %w", err)
	}
	go func() {
		_ = c.EvictIfNeeded()
	}()
	return data, nil
}

// resizeToHeight resizes src to targetH height, preserving aspect ratio,
// using CatmullRom resampling. If src is already at the right height,
// it is returned unmodified for callers that just need the bytes path.
func resizeToHeight(src image.Image, targetH int) image.Image {
	b := src.Bounds()
	srcH := b.Dy()
	srcW := b.Dx()
	if srcH <= 0 || srcW <= 0 {
		return src
	}
	targetW := int(float64(srcW)*float64(targetH)/float64(srcH) + 0.5)
	if targetW < 1 {
		targetW = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// encodeJPEG encodes img as JPEG at the cache quality.
func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeAtomic writes data to a temp file in the same directory as path
// and renames it into place, so concurrent readers never see a partial
// file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".thumb-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// Stats returns the current count and total bytes on disk for cache
// entries (.jpg files in Dir).
func (c *Cache) Stats() (count int, totalBytes int64, err error) {
	entries, err := listCacheEntries(c.opts.Dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		count++
		totalBytes += e.size
	}
	return count, totalBytes, nil
}

// EvictIfNeeded enforces the size cap by removing oldest-mtime entries
// until total bytes <= MaxBytes. It is idempotent and safe to call
// concurrently with Get.
func (c *Cache) EvictIfNeeded() error {
	entries, err := listCacheEntries(c.opts.Dir)
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	if total <= c.opts.MaxBytes {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime.Before(entries[j].mtime)
	})
	for _, e := range entries {
		if total <= c.opts.MaxBytes {
			break
		}
		if err := os.Remove(e.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("evict %s: %w", e.path, err)
		}
		total -= e.size
	}
	return nil
}

// cacheEntry describes one cached thumbnail file on disk.
type cacheEntry struct {
	path  string
	size  int64
	mtime time.Time
}

// listCacheEntries returns metadata for all .jpg files directly in dir.
func listCacheEntries(dir string) ([]cacheEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read cache dir: %w", err)
	}
	out := make([]cacheEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		if filepath.Ext(de.Name()) != ".jpg" {
			continue
		}
		info, err := de.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat cache entry: %w", err)
		}
		out = append(out, cacheEntry{
			path:  filepath.Join(dir, de.Name()),
			size:  info.Size(),
			mtime: info.ModTime(),
		})
	}
	return out, nil
}

// singleflight is a minimal in-memory deduplicator for concurrent calls
// keyed by string. Only one fn runs per key at a time; concurrent
// callers wait for and share the result.
type singleflight struct {
	mu    sync.Mutex
	calls map[string]*sfCall
}

// sfCall holds the in-flight result for a singleflight key.
type sfCall struct {
	done chan struct{}
	val  []byte
	err  error
}

// Do runs fn keyed by key, deduplicating concurrent calls.
func (s *singleflight) Do(key string, fn func() ([]byte, error)) ([]byte, error) {
	s.mu.Lock()
	if call, ok := s.calls[key]; ok {
		s.mu.Unlock()
		<-call.done
		return call.val, call.err
	}
	call := &sfCall{done: make(chan struct{})}
	s.calls[key] = call
	s.mu.Unlock()

	call.val, call.err = fn()
	close(call.done)

	s.mu.Lock()
	delete(s.calls, key)
	s.mu.Unlock()
	return call.val, call.err
}
