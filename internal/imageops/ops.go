// Package imageops provides image transformation operations used by splat.
//
// Each operation implements the Operation interface and transforms an
// image.Image into a new image.Image. Implementations are immutable value
// types and safe for concurrent use.
//
// All operations accept any image.Image input (RGBA, NRGBA, Gray, etc. —
// access is via image.At()). The returned image is always a fresh
// *image.RGBA for simplicity and to avoid encoders mishandling sub-image
// views that share pixel storage with the input.
//
// Internally only two primitives are implemented — a 90° clockwise rotation
// and a horizontal flip. Counter-clockwise, 180° and vertical flip are
// expressed as compositions of those two.
package imageops

import (
	"fmt"
	"image"
	"image/draw"
)

// Operation transforms an image.Image into a new image.Image.
// Implementations are immutable value types and safe for concurrent use.
type Operation interface {
	Name() string
	Apply(img image.Image) (image.Image, error)
}

// --- Crop -------------------------------------------------------------------

// Crop crops the image to the rectangle starting at (X,Y) with width W and
// height H, in original-image pixel coordinates (relative to the image's
// bounds origin).
type Crop struct{ X, Y, W, H int }

// Name returns the protocol name "crop".
func (Crop) Name() string { return "crop" }

// Apply returns a fresh *image.RGBA containing the requested sub-rectangle.
// It does not use SubImage (which would return a view sharing pixels with
// the source) — the result is always an independently allocated image.
func (c Crop) Apply(img image.Image) (image.Image, error) {
	if c.W <= 0 || c.H <= 0 {
		return nil, fmt.Errorf("imageops: crop W and H must be positive (got W=%d, H=%d)", c.W, c.H)
	}
	if c.X < 0 || c.Y < 0 {
		return nil, fmt.Errorf("imageops: crop X and Y must be non-negative (got X=%d, Y=%d)", c.X, c.Y)
	}
	b := img.Bounds()
	if c.X+c.W > b.Dx() {
		return nil, fmt.Errorf("imageops: crop X+W=%d exceeds image width %d", c.X+c.W, b.Dx())
	}
	if c.Y+c.H > b.Dy() {
		return nil, fmt.Errorf("imageops: crop Y+H=%d exceeds image height %d", c.Y+c.H, b.Dy())
	}

	dst := image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	// Source point is offset by the image's bounds Min so that crop
	// coordinates are interpreted relative to the visible bounds origin
	// regardless of the underlying bounds (which need not start at 0,0).
	srcPt := image.Pt(b.Min.X+c.X, b.Min.Y+c.Y)
	draw.Draw(dst, dst.Bounds(), img, srcPt, draw.Src)
	return dst, nil
}

// --- Rotation primitives ----------------------------------------------------

// rotateCW90 is the single rotation primitive: 90° clockwise.
//
// For an input with bounds Dx()=w, Dy()=h the output has dimensions h×w.
// A source pixel at (x,y) (relative to bounds.Min) maps to output (h-1-y, x).
func rotateCW90(img image.Image) *image.RGBA {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// flipHorizontal is the single horizontal-flip primitive.
// A source pixel at (x,y) maps to output (w-1-x, y).
func flipHorizontal(img image.Image) *image.RGBA {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// --- Public rotation operations --------------------------------------------

// RotateCW90 rotates the image 90° clockwise.
type RotateCW90 struct{}

// Name returns the protocol name "rotate-cw".
func (RotateCW90) Name() string { return "rotate-cw" }

// Apply returns the input rotated 90° clockwise as a fresh *image.RGBA.
func (RotateCW90) Apply(img image.Image) (image.Image, error) {
	return rotateCW90(img), nil
}

// RotateCCW90 rotates the image 90° counter-clockwise.
// Implemented as three CW90 rotations.
type RotateCCW90 struct{}

// Name returns the protocol name "rotate-ccw".
func (RotateCCW90) Name() string { return "rotate-ccw" }

// Apply returns the input rotated 90° counter-clockwise as a fresh *image.RGBA.
func (RotateCCW90) Apply(img image.Image) (image.Image, error) {
	return rotateCW90(rotateCW90(rotateCW90(img))), nil
}

// Rotate180 rotates the image 180°.
// Implemented as two CW90 rotations.
type Rotate180 struct{}

// Name returns the protocol name "rotate-180".
func (Rotate180) Name() string { return "rotate-180" }

// Apply returns the input rotated 180° as a fresh *image.RGBA.
func (Rotate180) Apply(img image.Image) (image.Image, error) {
	return rotateCW90(rotateCW90(img)), nil
}

// --- Public flip operations -------------------------------------------------

// FlipHorizontal mirrors the image along the vertical axis (left ↔ right).
type FlipHorizontal struct{}

// Name returns the protocol name "flip-h".
func (FlipHorizontal) Name() string { return "flip-h" }

// Apply returns the input flipped horizontally as a fresh *image.RGBA.
func (FlipHorizontal) Apply(img image.Image) (image.Image, error) {
	return flipHorizontal(img), nil
}

// FlipVertical mirrors the image along the horizontal axis (top ↔ bottom).
// Implemented as Rotate180 followed by horizontal flip.
type FlipVertical struct{}

// Name returns the protocol name "flip-v".
func (FlipVertical) Name() string { return "flip-v" }

// Apply returns the input flipped vertically as a fresh *image.RGBA.
func (FlipVertical) Apply(img image.Image) (image.Image, error) {
	return flipHorizontal(rotateCW90(rotateCW90(img))), nil
}
