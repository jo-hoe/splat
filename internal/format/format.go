// Package format provides a small registry of image format handlers
// (decode, encode, content-type, output-extension override) for the
// formats splat supports as input: JPEG, PNG, and WebP.
//
// The registry is constructed once at startup and is immutable thereafter.
// Adding a new format means adding one registration in NewRegistry.
package format

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sort"
	"strings"

	xwebp "golang.org/x/image/webp"
)

// Format describes how to encode/decode one image format.
//
// Ext is the canonical lowercase extension with leading dot, e.g. ".jpg".
// Aliases lists additional accepted extensions that map to this same Format
// (e.g. ".jpeg" is an alias for ".jpg").
//
// OutputExt is "" when the output extension equals Ext. A non-empty value
// forces a different output extension on save; this is used for WebP, which
// is decoded as input but written out as PNG.
//
// Encode is always non-nil. For formats that splat does not encode (WebP),
// Encode returns a sentinel error and callers must consult OutputFor to find
// the actual encoder.
type Format struct {
	Ext         string
	Aliases     []string
	ContentType string
	Decode      func(io.Reader) (image.Image, error)
	Encode      func(io.Writer, image.Image) error
	OutputExt   string
}

// ErrWebPEncodeUnsupported is returned by the WebP Format's Encode function.
// Callers should obtain the actual output Format via Registry.OutputFor.
var ErrWebPEncodeUnsupported = errors.New("format: webp encoding not supported")

// Registry is an immutable, lookup-by-extension set of image Formats.
//
// Construct via NewRegistry. The registry is safe for concurrent reads
// because it is never mutated after construction.
type Registry struct {
	// byExt maps every accepted extension (canonical Ext and every alias,
	// all lowercase, leading dot) to the Format it belongs to. Multiple
	// keys can map to the same Format value.
	byExt map[string]Format
	// canonical is the set of canonical Ext values (one per Format),
	// kept so we can iterate distinct formats if needed.
	canonical []Format
}

// NewRegistry returns a Registry pre-populated with handlers for JPEG, PNG,
// and WebP. jpegQuality is passed to the JPEG encoder and must be in [1, 100];
// otherwise an error is returned. (This validation duplicates the config-load
// check on purpose: it keeps the package self-contained for tests and for any
// future caller that builds a registry outside the normal config path.)
func NewRegistry(jpegQuality int) (*Registry, error) {
	if jpegQuality < 1 || jpegQuality > 100 {
		return nil, fmt.Errorf("format: jpegQuality must be in [1, 100], got %d", jpegQuality)
	}

	jpegOpts := &jpeg.Options{Quality: jpegQuality}

	formats := []Format{
		{
			Ext:         ".jpg",
			Aliases:     []string{".jpeg"},
			ContentType: "image/jpeg",
			Decode:      jpeg.Decode,
			Encode: func(w io.Writer, img image.Image) error {
				return jpeg.Encode(w, img, jpegOpts)
			},
			OutputExt: "",
		},
		{
			Ext:         ".png",
			ContentType: "image/png",
			Decode:      png.Decode,
			Encode:      png.Encode,
			OutputExt:   "",
		},
		{
			Ext:         ".webp",
			ContentType: "image/webp",
			Decode:      xwebp.Decode,
			Encode: func(_ io.Writer, _ image.Image) error {
				return ErrWebPEncodeUnsupported
			},
			OutputExt: ".png",
		},
	}

	r := &Registry{
		byExt:     make(map[string]Format, len(formats)*2),
		canonical: make([]Format, 0, len(formats)),
	}
	for _, f := range formats {
		r.canonical = append(r.canonical, f)
		r.byExt[f.Ext] = f
		for _, a := range f.Aliases {
			r.byExt[a] = f
		}
	}
	return r, nil
}

// ByExt returns the Format registered for the given extension. The lookup
// is case-insensitive; a leading dot is required (".jpg", not "jpg").
// Returns ok=false when the extension is not registered.
func (r *Registry) ByExt(ext string) (Format, bool) {
	f, ok := r.byExt[strings.ToLower(ext)]
	return f, ok
}

// IsSupported reports whether ext is a supported source extension.
func (r *Registry) IsSupported(ext string) bool {
	_, ok := r.ByExt(ext)
	return ok
}

// SupportedExtensions returns every canonical and alias extension currently
// registered, sorted lexicographically. The returned slice is freshly
// allocated and may be modified by the caller.
func (r *Registry) SupportedExtensions() []string {
	out := make([]string, 0, len(r.byExt))
	for k := range r.byExt {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// OutputFor returns the Format and extension to use when writing a file
// whose input extension is inputExt. For most formats the output is the
// same as the input (e.g. .jpg in, .jpg out). For WebP, OutputFor returns
// the PNG Format and ".png".
//
// Returns ok=false when inputExt is not a registered extension.
func (r *Registry) OutputFor(inputExt string) (Format, string, bool) {
	in, ok := r.ByExt(inputExt)
	if !ok {
		return Format{}, "", false
	}
	if in.OutputExt == "" {
		return in, in.Ext, true
	}
	out, ok := r.ByExt(in.OutputExt)
	if !ok {
		// This indicates a programming error in NewRegistry — a Format
		// declared an OutputExt that isn't registered. Surface it rather
		// than silently returning the wrong handler.
		return Format{}, "", false
	}
	return out, in.OutputExt, true
}
