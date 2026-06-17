package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocalSource is a Source backed by a directory on the local filesystem.
//
// Keys are canonical slash-separated paths relative to the configured
// root. Path-traversal attempts (keys that resolve outside root) are
// rejected with ErrInvalidKey.
type LocalSource struct {
	root    string // original input
	rootAbs string // cleaned absolute path used for traversal checks
}

// NewLocalSource creates a LocalSource rooted at root. Root must
// already exist and be a directory; otherwise an error is returned.
func NewLocalSource(root string) (*LocalSource, error) {
	if root == "" {
		return nil, errors.New("source: local root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("source: resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("source: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source: root %q is not a directory", root)
	}
	return &LocalSource{root: root, rootAbs: filepath.Clean(abs)}, nil
}

// List walks the source tree and returns all files whose extensions are
// allowlisted, sorted alphabetically by Key.
func (l *LocalSource) List(ctx context.Context) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(l.rootAbs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !IsAllowedExt(ext) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		key, err := l.pathToKey(p)
		if err != nil {
			return err
		}
		entries = append(entries, Entry{
			Key:     key,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			ETag:    fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano()),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}

// Get opens the file at key and returns a reader plus its metadata.
func (l *LocalSource) Get(ctx context.Context, key string) (io.ReadCloser, Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, Metadata{}, err
	}
	abs, _, err := l.resolveKey(key)
	if err != nil {
		return nil, Metadata{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Metadata{}, ErrNotFound
		}
		return nil, Metadata{}, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Metadata{}, err
	}
	meta := Metadata{
		Size:        info.Size(),
		ContentType: contentTypeForKey(key),
		ETag:        fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano()),
	}
	return f, meta, nil
}

// Exists reports whether the file for key is present.
func (l *LocalSource) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	abs, _, err := l.resolveKey(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(abs)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Put writes the bytes from r to the path for key, atomically replacing
// any existing file via write-temp-then-rename. The contentType
// parameter is accepted for interface symmetry with the S3 source but
// is not stored on the local filesystem.
func (l *LocalSource) Put(ctx context.Context, key string, r io.Reader, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, _, err := l.resolveKey(key)
	if err != nil {
		return err
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, filepath.Base(abs)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := writeAndRename(tmp, tmpPath, abs, r); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Delete removes the file at key.
func (l *LocalSource) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, _, err := l.resolveKey(key)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// writeAndRename copies r into tmp, closes it, and renames it onto
// final. tmp is closed even on error paths.
func writeAndRename(tmp *os.File, tmpPath, final string, r io.Reader) error {
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, final)
}

// resolveKey validates key, builds the absolute filesystem path, and
// confirms the path is still under root. Returns the absolute path,
// the cleaned key, and any error.
func (l *LocalSource) resolveKey(key string) (absPath, cleanKey string, err error) {
	cleaned, err := CleanKey(key)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Clean(filepath.Join(l.rootAbs, filepath.FromSlash(cleaned)))
	if !isUnderRoot(abs, l.rootAbs) {
		return "", "", ErrInvalidKey
	}
	return abs, cleaned, nil
}

// isUnderRoot reports whether abs is rootAbs itself or descends from it,
// using a separator-aware prefix check.
func isUnderRoot(abs, rootAbs string) bool {
	if abs == rootAbs {
		return true
	}
	prefix := rootAbs
	if !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}
	return strings.HasPrefix(abs, prefix)
}

// pathToKey converts an absolute filesystem path under root to a
// canonical, slash-separated, source-relative key.
func (l *LocalSource) pathToKey(abs string) (string, error) {
	rel, err := filepath.Rel(l.rootAbs, abs)
	if err != nil {
		return "", err
	}
	key := filepath.ToSlash(rel)
	key = strings.TrimPrefix(key, "/")
	return key, nil
}

// contentTypeForKey returns the MIME type for a key based on its
// extension. Unknown extensions map to "application/octet-stream".
func contentTypeForKey(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
