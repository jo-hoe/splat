package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalWebPHex is the same 38-byte VP8L (lossless) WebP fixture as in
// internal/format/format_test.go: a 4x4 white image. Copied here so the
// server tests do not depend on a sibling _test.go file.
const minimalWebPHex = "524946461e000000574542505650384c110000002f03c0000007d0fffef7bfff8188e87f0000"

// computeHashOf returns the SHA-256 hex of the file at path.
func computeHashOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// applyFormBody encodes a map as application/x-www-form-urlencoded.
func applyFormBody(values map[string]string) string {
	v := url.Values{}
	for k, val := range values {
		v.Set(k, val)
	}
	return v.Encode()
}

// doForm posts a urlencoded form to the handler.
func doForm(t *testing.T, h http.Handler, target string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	body := strings.NewReader(applyFormBody(form))
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	return rr
}

// doDelete issues a DELETE request to the handler.
func doDelete(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	h.ServeHTTP(rr, req)
	return rr
}

// minimalWebP returns the bytes of the embedded 4x4 lossless WebP fixture.
func minimalWebP(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(minimalWebPHex)
	if err != nil {
		t.Fatalf("decode webp hex: %v", err)
	}
	return b
}

func TestEditor(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	hash := computeHashOf(t, filepath.Join(srcRoot, "a.png"))

	rr := do(t, srv.Handler(), http.MethodGet, "/editor/a.png")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	wants := []string{
		`id="editor-pane"`,
		`data-key="a.png"`,
		`data-hash="` + hash + `"`,
		`class="ratio-btn"`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q\nbody=%s", w, body)
		}
	}
}

func TestThumb(t *testing.T) {
	srv, _ := newTestServer(t)
	cacheDir := srv.cfg.Thumbnails.CacheDir

	rr := do(t, srv.Handler(), http.MethodGet, "/thumb/a.png")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", got)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("empty thumb body")
	}
	if _, err := jpeg.Decode(bytes.NewReader(rr.Body.Bytes())); err != nil {
		t.Fatalf("decode jpeg thumb: %v", err)
	}

	// Verify the cache directory now holds at least one file.
	count := 0
	err := filepath.Walk(cacheDir, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache dir: %v", err)
	}
	if count == 0 {
		t.Errorf("cache dir %s contains no files after thumb request", cacheDir)
	}
}

// TestThumbETagRevalidation verifies the thumbnail endpoint emits an ETag and
// honors If-None-Match with a 304, so browsers can revalidate cheaply.
func TestThumbETagRevalidation(t *testing.T) {
	srv, _ := newTestServer(t)

	rr := do(t, srv.Handler(), http.MethodGet, "/thumb/a.png")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("thumb response missing ETag header")
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want it to contain no-cache", cc)
	}

	// A conditional request with the current ETag must return 304 Not Modified.
	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/thumb/a.png", nil)
	req.Header.Set("If-None-Match", etag)
	srv.Handler().ServeHTTP(rr2, req)
	if rr2.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", rr2.Code)
	}
}

// TestThumbRefreshesAfterInPlaceEdit is the regression test for stale previews:
// after an in-place edit the source ETag changes, so the thumbnail endpoint must
// return a new ETag and freshly rendered bytes rather than the cached ones.
func TestThumbRefreshesAfterInPlaceEdit(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	h := srv.Handler()

	// Prime the thumbnail cache and capture the pre-edit ETag + bytes.
	before := do(t, h, http.MethodGet, "/thumb/a.png")
	if before.Code != http.StatusOK {
		t.Fatalf("pre-edit thumb status = %d, want 200", before.Code)
	}
	etagBefore := before.Header().Get("ETag")
	bytesBefore := append([]byte(nil), before.Body.Bytes()...)

	// Apply an in-place crop to a.png. The local source writes new bytes with a
	// new mtime, which changes its ETag.
	hash := computeHashOf(t, filepath.Join(srcRoot, "a.png"))
	applyRR := doForm(t, h, "/apply/a.png", map[string]string{
		"op":   "crop",
		"mode": "inplace",
		"hash": hash,
		"x":    "0",
		"y":    "0",
		"w":    "2",
		"h":    "2",
	})
	if applyRR.Code != http.StatusOK {
		t.Fatalf("apply status = %d, want 200\nbody=%s", applyRR.Code, applyRR.Body.String())
	}

	after := do(t, h, http.MethodGet, "/thumb/a.png")
	if after.Code != http.StatusOK {
		t.Fatalf("post-edit thumb status = %d, want 200", after.Code)
	}
	etagAfter := after.Header().Get("ETag")
	bytesAfter := after.Body.Bytes()

	if etagAfter == etagBefore {
		t.Errorf("thumb ETag did not change after in-place edit: %q", etagAfter)
	}
	if bytes.Equal(bytesAfter, bytesBefore) {
		t.Error("thumb bytes unchanged after in-place edit (stale cached thumbnail served)")
	}

	// The stale pre-edit ETag must no longer validate (no accidental 304).
	stale := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/thumb/a.png", nil)
	req.Header.Set("If-None-Match", etagBefore)
	h.ServeHTTP(stale, req)
	if stale.Code == http.StatusNotModified {
		t.Error("pre-edit ETag still returns 304 after edit; browser would keep the stale thumb")
	}
}

func TestImage(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := do(t, srv.Handler(), http.MethodGet, "/image/a.png")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if _, err := png.Decode(bytes.NewReader(rr.Body.Bytes())); err != nil {
		t.Fatalf("decode png: %v", err)
	}
}

func TestApply_Crop_Inplace_HappyPath(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	srcPath := filepath.Join(srcRoot, "a.png")
	hash := computeHashOf(t, srcPath)
	originalBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	form := map[string]string{
		"op":   "crop",
		"mode": "inplace",
		"hash": hash,
		"x":    "0",
		"y":    "0",
		"w":    "2",
		"h":    "2",
	}
	rr := doForm(t, srv.Handler(), "/apply/a.png", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Saved") {
		t.Errorf("body missing 'Saved'\nbody=%s", rr.Body.String())
	}
	if trig := rr.Header().Get("HX-Trigger"); !strings.Contains(trig, "showToast") {
		t.Errorf("HX-Trigger = %q, want it to contain 'showToast'", trig)
	}

	// File contents must have changed.
	newBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read post-apply: %v", err)
	}
	if bytes.Equal(originalBytes, newBytes) {
		t.Fatal("file contents unchanged after crop")
	}
	// Decoded image must be 2x2.
	img, err := png.Decode(bytes.NewReader(newBytes))
	if err != nil {
		t.Fatalf("decode post-apply: %v", err)
	}
	if got := img.Bounds().Dx(); got != 2 {
		t.Errorf("width = %d, want 2", got)
	}
	if got := img.Bounds().Dy(); got != 2 {
		t.Errorf("height = %d, want 2", got)
	}
}

func TestApply_Crop_HashMismatch_Returns409(t *testing.T) {
	srv, _ := newTestServer(t)
	form := map[string]string{
		"op":   "crop",
		"mode": "inplace",
		"hash": strings.Repeat("0", 64),
		"x":    "0",
		"y":    "0",
		"w":    "2",
		"h":    "2",
	}
	rr := doForm(t, srv.Handler(), "/apply/a.png", form)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "modified elsewhere") {
		t.Errorf("body missing 'modified elsewhere'\nbody=%s", rr.Body.String())
	}
}

func TestApply_Copy_CreatesEditedSibling(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	srcPath := filepath.Join(srcRoot, "a.png")
	hash := computeHashOf(t, srcPath)
	originalBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	form := map[string]string{
		"op":   "rotate-cw",
		"mode": "copy",
		"hash": hash,
	}
	rr := doForm(t, srv.Handler(), "/apply/a.png", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	editedPath := filepath.Join(srcRoot, "a-edited.png")
	if _, err := os.Stat(editedPath); err != nil {
		t.Fatalf("expected %s to exist: %v", editedPath, err)
	}
	currentBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source after copy: %v", err)
	}
	if !bytes.Equal(originalBytes, currentBytes) {
		t.Fatal("source a.png was modified by copy mode; want unchanged")
	}
}

func TestApply_Copy_CollisionGetsCounter(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	// Pre-create the colliding edited sibling.
	writePNG(t, filepath.Join(srcRoot, "a-edited.png"), 4, 4)

	hash := computeHashOf(t, filepath.Join(srcRoot, "a.png"))
	form := map[string]string{
		"op":   "rotate-cw",
		"mode": "copy",
		"hash": hash,
	}
	rr := doForm(t, srv.Handler(), "/apply/a.png", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	counterPath := filepath.Join(srcRoot, "a-edited-1.png")
	if _, err := os.Stat(counterPath); err != nil {
		t.Fatalf("expected %s to exist: %v", counterPath, err)
	}
}

func TestApply_Webp_To_Png_Inplace(t *testing.T) {
	srv, srcRoot := newTestServer(t)

	webpPath := filepath.Join(srcRoot, "pic.webp")
	if err := os.WriteFile(webpPath, minimalWebP(t), 0o644); err != nil {
		t.Fatalf("write webp: %v", err)
	}
	hash := computeHashOf(t, webpPath)

	form := map[string]string{
		"op":   "rotate-cw",
		"mode": "inplace",
		"hash": hash,
	}
	rr := doForm(t, srv.Handler(), "/apply/pic.webp", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	pngPath := filepath.Join(srcRoot, "pic.png")
	if _, err := os.Stat(pngPath); err != nil {
		t.Fatalf("expected %s to exist: %v", pngPath, err)
	}
	if _, err := os.Stat(webpPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", webpPath, err)
	}
	// Confirm the new png is a valid image.
	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatalf("read png: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("decode resulting png: %v", err)
	}
}

// TestApply_Jpeg_Inplace_KeepsExtension is a regression test for the bug where
// cropping a ".jpeg" file in place renamed it to ".jpg" and deleted the
// original. Because ".jpeg" is a registry *alias* of the ".jpg" Format,
// OutputFor(".jpeg") returns the canonical ".jpg", so computeTargetKey saw an
// "extension change" and (1) wrote a NEW ".jpg" file and (2) deleted the source
// ".jpeg". The strip thumbnail still carried the ".jpeg" key, so re-selecting
// the just-edited image 404'd: "it was in fact edited but could not be selected
// after."
//
// Correct behaviour: an in-place save of a .jpeg stays .jpeg (same underlying
// format, no rename, original preserved).
func TestApply_Jpeg_Inplace_KeepsExtension(t *testing.T) {
	srv, srcRoot := newTestServer(t)

	jpegPath := filepath.Join(srcRoot, "photo.jpeg")
	writeJPEG(t, jpegPath, 4, 4)
	hash := computeHashOf(t, jpegPath)

	form := map[string]string{
		"op":   "crop",
		"mode": "inplace",
		"hash": hash,
		"x":    "0",
		"y":    "0",
		"w":    "2",
		"h":    "2",
	}
	rr := doForm(t, srv.Handler(), "/apply/photo.jpeg", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// The original .jpeg must still exist (not renamed/deleted).
	if _, err := os.Stat(jpegPath); err != nil {
		t.Fatalf("expected %s to still exist after in-place crop: %v", jpegPath, err)
	}
	// No sibling .jpg must have been created.
	if _, err := os.Stat(filepath.Join(srcRoot, "photo.jpg")); !os.IsNotExist(err) {
		t.Fatalf("expected no photo.jpg to be created, stat err = %v", err)
	}
	// The success fragment's target must be the original .jpeg key so the strip
	// thumbnail still resolves (the whole point: re-selecting it must not 404).
	if !strings.Contains(rr.Body.String(), "photo.jpeg") {
		t.Errorf("success fragment target is not photo.jpeg\nbody=%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "photo.jpg\"") {
		t.Errorf("success fragment unexpectedly references photo.jpg\nbody=%s", rr.Body.String())
	}

	// The edited file must decode and be cropped to 2x2.
	newBytes, err := os.ReadFile(jpegPath)
	if err != nil {
		t.Fatalf("read post-apply: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(newBytes))
	if err != nil {
		t.Fatalf("decode post-apply jpeg: %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Errorf("cropped size = %dx%d, want 2x2", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestDelete_HappyPath(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	rr := doDelete(t, srv.Handler(), "/image/a.png")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if trig := rr.Header().Get("HX-Trigger"); trig == "" {
		t.Error("HX-Trigger header missing on delete")
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "a.png")); !os.IsNotExist(err) {
		t.Errorf("expected a.png removed; stat err = %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := doDelete(t, srv.Handler(), "/image/missing.png")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestStrip_Pagination(t *testing.T) {
	srv, srcRoot := newTestServer(t)

	// newTestServer seeds a.png, b.png, nested/c.png. Add two more PNGs
	// so the alphabetical list has at least 5 entries, then walk.
	writePNG(t, filepath.Join(srcRoot, "d.png"), 4, 4)
	writePNG(t, filepath.Join(srcRoot, "e.png"), 4, 4)

	// List order from source.List is alphabetical:
	//   [0] a.png, [1] b.png, [2] d.png, [3] e.png, [4] nested/c.png
	rr := do(t, srv.Handler(), http.MethodGet, "/strip?offset=2&limit=2")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	wants := []string{
		`data-key="d.png"`,
		`data-key="e.png"`,
		`strip-loader`,
		`offset=4`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q\nbody=%s", w, body)
		}
	}
	// And entries at offsets outside [2,4) should NOT appear.
	for _, unwanted := range []string{`data-key="a.png"`, `data-key="b.png"`, `data-key="nested/c.png"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body unexpectedly contains %q", unwanted)
		}
	}
}

func TestApply_BadOp(t *testing.T) {
	srv, srcRoot := newTestServer(t)
	hash := computeHashOf(t, filepath.Join(srcRoot, "a.png"))
	form := map[string]string{
		"op":   "garbage",
		"mode": "inplace",
		"hash": hash,
	}
	rr := doForm(t, srv.Handler(), "/apply/a.png", form)
	// renderError uses HTTP 200 with an inline error fragment.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Error:") {
		t.Errorf("body missing 'Error:'\nbody=%s", rr.Body.String())
	}
}
