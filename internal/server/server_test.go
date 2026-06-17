package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jo-hoe/splat/internal/config"
	"github.com/jo-hoe/splat/internal/format"
	"github.com/jo-hoe/splat/internal/source"
	"github.com/jo-hoe/splat/internal/thumbs"
)

// newTestServer constructs a Server backed by a t.TempDir local source
// pre-populated with a few small PNG files. Returns the server and the
// source root for direct filesystem assertions.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	srcRoot := t.TempDir()
	cacheRoot := t.TempDir()
	writePNG(t, filepath.Join(srcRoot, "a.png"), 4, 4)
	writePNG(t, filepath.Join(srcRoot, "b.png"), 4, 4)
	if err := os.MkdirAll(filepath.Join(srcRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writePNG(t, filepath.Join(srcRoot, "nested", "c.png"), 4, 4)

	cfg := &config.Config{
		Server: config.ServerConfig{Port: 0},
		Source: config.SourceConfig{
			Type:  config.SourceLocal,
			Local: &config.LocalConfig{Root: srcRoot},
		},
		Editing: config.EditingConfig{CopySuffix: "-edited", JPEGQuality: 90},
		Thumbnails: config.ThumbnailsConfig{
			CacheDir:      cacheRoot,
			HeightPx:      80,
			CacheMaxBytes: 10 * 1024 * 1024,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	src, err := source.NewLocalSource(srcRoot)
	if err != nil {
		t.Fatalf("local source: %v", err)
	}
	reg, err := format.NewRegistry(cfg.Editing.JPEGQuality)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	cache, err := thumbs.New(thumbs.Options{
		Dir:      cacheRoot,
		HeightPx: cfg.Thumbnails.HeightPx,
		MaxBytes: cfg.Thumbnails.CacheMaxBytes,
		SourceID: "local:" + srcRoot,
	})
	if err != nil {
		t.Fatalf("thumbs: %v", err)
	}
	srv, err := New(Options{
		Cfg:      cfg,
		Source:   src,
		Registry: reg,
		Thumbs:   cache,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv, srcRoot
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	h.ServeHTTP(rr, req)
	return rr
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := do(t, srv.Handler(), http.MethodGet, "/healthz")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestIndex(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := do(t, srv.Handler(), http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"<title>splat</title>", `id="strip"`, `id="editor"`, `hx-get="/strip"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestStrip(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := do(t, srv.Handler(), http.MethodGet, "/strip")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`data-key="a.png"`, `data-key="b.png"`, `data-key="nested/c.png"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}
