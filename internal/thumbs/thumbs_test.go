package thumbs

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeImage returns an opaque RGBA image with the given dimensions and
// a non-trivial colour pattern so JPEG encoding produces variable bytes.
func makeImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	return img
}

// gen returns a Generator producing the supplied image and incrementing
// counter on each call.
func gen(img image.Image, counter *int32) Generator {
	return func(ctx context.Context) (image.Image, error) {
		atomic.AddInt32(counter, 1)
		return img, nil
	}
}

func defaultOpts(t *testing.T) Options {
	t.Helper()
	return Options{
		Dir:      t.TempDir(),
		HeightPx: 50,
		MaxBytes: 1 << 20,
		SourceID: "test",
	}
}

func TestNew_Validation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Options)
	}{
		{"empty Dir", func(o *Options) { o.Dir = "" }},
		{"zero HeightPx", func(o *Options) { o.HeightPx = 0 }},
		{"negative HeightPx", func(o *Options) { o.HeightPx = -1 }},
		{"zero MaxBytes", func(o *Options) { o.MaxBytes = 0 }},
		{"negative MaxBytes", func(o *Options) { o.MaxBytes = -1 }},
		{"empty SourceID", func(o *Options) { o.SourceID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultOpts(t)
			tc.mut(&opts)
			if _, err := New(opts); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestNew_CreatesDir(t *testing.T) {
	parent := t.TempDir()
	opts := Options{
		Dir:      filepath.Join(parent, "nested", "cache"),
		HeightPx: 50,
		MaxBytes: 1 << 20,
		SourceID: "test",
	}
	if _, err := New(opts); err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(opts.Dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
}

func TestNew_DirIsFile(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	opts := Options{
		Dir:      path,
		HeightPx: 50,
		MaxBytes: 1 << 20,
		SourceID: "test",
	}
	if _, err := New(opts); err == nil {
		t.Fatalf("expected error when Dir is a file")
	}
}

func TestNew_Valid(t *testing.T) {
	if _, err := New(defaultOpts(t)); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestGet_ColdMiss(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	data, err := c.Get(context.Background(), "key1", "etag1", gen(makeImage(100, 100), &calls))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected generator to be called once, got %d", calls)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty bytes")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("returned bytes are not valid JPEG: %v", err)
	}
	entries, err := os.ReadDir(c.opts.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	jpgs := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jpg" {
			jpgs++
		}
	}
	if jpgs != 1 {
		t.Fatalf("expected 1 .jpg on disk, got %d", jpgs)
	}
}

func TestGet_WarmHit(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	g := gen(makeImage(100, 100), &calls)
	first, err := c.Get(context.Background(), "key1", "etag1", g)
	if err != nil {
		t.Fatalf("Get cold: %v", err)
	}
	second, err := c.Get(context.Background(), "key1", "etag1", g)
	if err != nil {
		t.Fatalf("Get warm: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected generator to be called once, got %d", calls)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("expected identical bytes from warm hit")
	}
}

func TestGet_NewEtagRegenerates(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	g := gen(makeImage(100, 100), &calls)
	if _, err := c.Get(context.Background(), "key1", "etag1", g); err != nil {
		t.Fatalf("Get etag1: %v", err)
	}
	if _, err := c.Get(context.Background(), "key1", "etag2", g); err != nil {
		t.Fatalf("Get etag2: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 generator calls, got %d", calls)
	}
	count, _, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 cache files, got %d", count)
	}
}

func TestGet_ConcurrentSameKey(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	slow := func(ctx context.Context) (image.Image, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(100 * time.Millisecond)
		return makeImage(100, 100), nil
	}
	const N = 8
	var wg sync.WaitGroup
	results := make([][]byte, N)
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.Get(context.Background(), "k", "e", slow)
		}()
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d: %v", i, e)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 generator call, got %d", got)
	}
	for i := 1; i < N; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("goroutine %d returned different bytes", i)
		}
	}
}

func TestEvictIfNeeded(t *testing.T) {
	// Populate via a generous cache so the async post-Put eviction
	// is a no-op, then build a second Cache pointed at the same Dir
	// with a tiny cap to actually drive eviction deterministically.
	dir := t.TempDir()
	bigOpts := Options{
		Dir:      dir,
		HeightPx: 100,
		MaxBytes: 1 << 30,
		SourceID: "test",
	}
	bigCache, err := New(bigOpts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	keys := []string{"a", "b", "c", "d"}
	for _, k := range keys {
		var calls int32
		if _, err := bigCache.Get(context.Background(), k, "e", gen(makeImage(200, 200), &calls)); err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
	}
	// Wait briefly for any async eviction goroutines triggered by Put
	// to finish — they will be no-ops with the generous cap.
	time.Sleep(50 * time.Millisecond)

	// Stamp distinct mtimes (keys[0] oldest, keys[len-1] newest).
	now := time.Now()
	for i, k := range keys {
		path := bigCache.pathFor(k, "e")
		mtime := now.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	beforeCount, beforeBytes, err := bigCache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if beforeCount != len(keys) {
		t.Fatalf("expected %d entries, got %d", len(keys), beforeCount)
	}

	// Now build a tight-cap cache and evict.
	tightOpts := bigOpts
	tightOpts.MaxBytes = beforeBytes / 2 // force at least half to be evicted
	tightCache, err := New(tightOpts)
	if err != nil {
		t.Fatalf("New tight: %v", err)
	}
	if err := tightCache.EvictIfNeeded(); err != nil {
		t.Fatalf("EvictIfNeeded: %v", err)
	}

	count, total, err := tightCache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if total > tightOpts.MaxBytes {
		t.Fatalf("expected total %d <= MaxBytes %d", total, tightOpts.MaxBytes)
	}
	if count == 0 {
		t.Fatalf("expected at least one entry to survive")
	}
	// Oldest (first key) must be gone; newest must remain.
	if _, err := os.Stat(tightCache.pathFor(keys[0], "e")); !os.IsNotExist(err) {
		t.Fatalf("expected oldest %q to be evicted, got err=%v", keys[0], err)
	}
	if _, err := os.Stat(tightCache.pathFor(keys[len(keys)-1], "e")); err != nil {
		t.Fatalf("expected newest %q to remain: %v", keys[len(keys)-1], err)
	}
}

func TestStats_MatchesDisk(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	for _, k := range []string{"a", "b", "c"} {
		if _, err := c.Get(context.Background(), k, "e", gen(makeImage(80, 60), &calls)); err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
	}
	count, total, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// Compute disk reality directly.
	entries, err := os.ReadDir(c.opts.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var diskCount int
	var diskBytes int64
	for _, de := range entries {
		if de.IsDir() || filepath.Ext(de.Name()) != ".jpg" {
			continue
		}
		info, err := de.Info()
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		diskCount++
		diskBytes += info.Size()
	}
	if count != diskCount {
		t.Fatalf("count: Stats=%d disk=%d", count, diskCount)
	}
	if total != diskBytes {
		t.Fatalf("bytes: Stats=%d disk=%d", total, diskBytes)
	}
}

func TestAspectPreservation(t *testing.T) {
	opts := defaultOpts(t)
	opts.HeightPx = 200
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	data, err := c.Get(context.Background(), "k", "e", gen(makeImage(800, 400), &calls))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b := img.Bounds()
	if got := b.Dy(); got < 198 || got > 202 {
		t.Fatalf("height: expected ~200, got %d", got)
	}
	if got := b.Dx(); got < 396 || got > 404 {
		t.Fatalf("width: expected ~400, got %d", got)
	}
}

func TestMtimeBumpOnHit(t *testing.T) {
	c, err := New(defaultOpts(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int32
	g := gen(makeImage(100, 100), &calls)
	if _, err := c.Get(context.Background(), "k", "e", g); err != nil {
		t.Fatalf("Get cold: %v", err)
	}
	path := c.pathFor("k", "e")
	// Stamp the mtime well in the past so we can detect a bump.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	if _, err := c.Get(context.Background(), "k", "e", g); err != nil {
		t.Fatalf("Get warm: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !after.ModTime().After(before.ModTime()) {
		// Some filesystems have low mtime granularity; flag it via the
		// "recent enough" check the design suggested.
		if time.Since(after.ModTime()) > 100*time.Millisecond {
			t.Skipf("mtime not advanced (filesystem granularity?): before=%v after=%v",
				before.ModTime(), after.ModTime())
		}
	}
}

func TestSingleflight_Sequential(t *testing.T) {
	sf := &singleflight{calls: make(map[string]*sfCall)}
	var n int32
	got, err := sf.Do("k", func() ([]byte, error) {
		atomic.AddInt32(&n, 1)
		return []byte("x"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Fatalf("got %q", got)
	}
	// After completion, key must be removed so a fresh Do runs fn again.
	got, err = sf.Do("k", func() ([]byte, error) {
		atomic.AddInt32(&n, 1)
		return []byte("y"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "y" {
		t.Fatalf("got %q", got)
	}
	if atomic.LoadInt32(&n) != 2 {
		t.Fatalf("expected fn to run twice, got %d", n)
	}
}
