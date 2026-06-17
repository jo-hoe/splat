// Package source defines the abstraction over backends that store images
// for the splat application. A Source is an addressable collection of
// image objects identified by canonical, slash-separated keys.
//
// Two implementations are planned: a local filesystem source (this
// package) and an S3 source (added in a separate file). Listings are
// filtered to the v1 extension allowlist (.jpg, .jpeg, .png, .webp)
// before being returned to callers.
package source

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"time"
)

// Entry is a single image listed by a Source.
//
// Key is canonical, slash-separated, and source-relative. It never starts
// with a leading "/" and never contains backslashes, "." segments, or
// ".." segments.
type Entry struct {
	// Key is the canonical, slash-separated, source-relative key.
	Key string
	// Size is the object size in bytes.
	Size int64
	// ModTime is the last-modified timestamp.
	ModTime time.Time
	// ETag is an opaque version token. For the local source this is
	// "<size>-<unix-nano>"; for S3 it is the object ETag.
	ETag string
}

// Metadata is returned alongside the bytes from Get.
type Metadata struct {
	// Size is the object size in bytes.
	Size int64
	// ContentType is the MIME type derived from the key extension.
	ContentType string
	// ETag is the same opaque version token surfaced in Entry.
	ETag string
}

// Source is a backend storing images, addressed by canonical keys.
//
// Implementations must filter listings to the v1 extension allowlist.
// All methods accept a context for cancellation and deadlines.
type Source interface {
	// List returns all entries in the source, filtered to the
	// allowlisted extensions, sorted alphabetically by Key.
	List(ctx context.Context) ([]Entry, error)
	// Get opens the object identified by key and returns a reader
	// over its bytes plus its metadata. The caller must Close the
	// reader. Returns ErrNotFound if the key does not exist and
	// ErrInvalidKey if the key fails canonicalization.
	Get(ctx context.Context, key string) (io.ReadCloser, Metadata, error)
	// Exists reports whether the given key is present in the source.
	// A non-existent key returns (false, nil), not an error.
	Exists(ctx context.Context, key string) (bool, error)
	// Put writes the bytes from r to the given key with the given
	// content type. Existing objects at the key are overwritten
	// atomically.
	Put(ctx context.Context, key string, r io.Reader, contentType string) error
	// Delete removes the object at the given key. Returns
	// ErrNotFound if the key does not exist.
	Delete(ctx context.Context, key string) error
}

// Sentinel errors returned by Source implementations.
var (
	// ErrNotFound indicates the requested key does not exist.
	ErrNotFound = errors.New("source: not found")
	// ErrInvalidKey indicates a key failed canonicalization rules.
	ErrInvalidKey = errors.New("source: invalid key")
)

// AllowedExtensions is the v1 extension allowlist. Each entry is
// lowercase and includes the leading dot.
var AllowedExtensions = []string{".jpg", ".jpeg", ".png", ".webp"}

// IsAllowedExt reports whether ext (which must include a leading dot)
// is one of the allowed extensions. The comparison is case-insensitive.
func IsAllowedExt(ext string) bool {
	if ext == "" {
		return false
	}
	lower := strings.ToLower(ext)
	for _, a := range AllowedExtensions {
		if a == lower {
			return true
		}
	}
	return false
}

// CleanKey canonicalizes a candidate key.
//
// The returned key is slash-separated, has no leading slash, no ".."
// segments, no "." segments, no empty segments, and no backslashes.
// The extension portion is lowercased; the rest of the filename's case
// is preserved. CleanKey returns ErrInvalidKey on rejection.
func CleanKey(raw string) (string, error) {
	if raw == "" {
		return "", ErrInvalidKey
	}
	if strings.ContainsRune(raw, '\\') {
		return "", ErrInvalidKey
	}
	if strings.HasPrefix(raw, "/") {
		return "", ErrInvalidKey
	}
	parts := strings.Split(raw, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", ErrInvalidKey
		}
	}
	cleaned := strings.Join(parts, "/")
	return lowercaseExt(cleaned), nil
}

// lowercaseExt lowercases the extension of a key (the substring from
// the last "." onwards) while preserving the rest of the casing.
func lowercaseExt(key string) string {
	ext := path.Ext(key)
	if ext == "" {
		return key
	}
	return strings.TrimSuffix(key, ext) + strings.ToLower(ext)
}
