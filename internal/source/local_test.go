package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustWrite writes data to filepath.Join(dir, rel...) creating parent
// directories as needed. Test fatal on error.
func mustWrite(t *testing.T, dir string, rel string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", p, err)
	}
	return p
}

func TestNewLocalSource_MissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NewLocalSource(missing); err == nil {
		t.Fatalf("expected error for missing root")
	}
}

func TestNewLocalSource_RootIsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewLocalSource(f); err == nil {
		t.Fatalf("expected error for non-dir root")
	}
}

func TestNewLocalSource_EmptyRoot(t *testing.T) {
	if _, err := NewLocalSource(""); err == nil {
		t.Fatalf("expected error for empty root")
	}
}

func TestLocalSource_List_FilteringAndSort(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "z.png", []byte("z"))
	mustWrite(t, dir, "a.jpg", []byte("a"))
	mustWrite(t, dir, "b.gif", []byte("b"))   // disallowed
	mustWrite(t, dir, "c.txt", []byte("c"))   // disallowed
	mustWrite(t, dir, "m.JPEG", []byte("m"))  // mixed case ext, allowed
	mustWrite(t, dir, "n.WEBP", []byte("n"))  // mixed case ext, allowed

	src, err := NewLocalSource(dir)
	if err != nil {
		t.Fatalf("NewLocalSource: %v", err)
	}
	entries, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotKeys := make([]string, len(entries))
	for i, e := range entries {
		gotKeys[i] = e.Key
	}
	wantKeys := []string{"a.jpg", "m.JPEG", "n.WEBP", "z.png"}
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("keys = %v, want %v", gotKeys, wantKeys)
	}
}

func TestLocalSource_List_Recursive(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "top.jpg", []byte("t"))
	mustWrite(t, dir, "sub/inner.png", []byte("i"))
	mustWrite(t, dir, "sub/deeper/leaf.webp", []byte("l"))

	src, _ := NewLocalSource(dir)
	entries, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%v)", len(entries), entries)
	}
	want := []string{"sub/deeper/leaf.webp", "sub/inner.png", "top.jpg"}
	for i, e := range entries {
		if e.Key != want[i] {
			t.Errorf("entry[%d].Key = %q, want %q", i, e.Key, want[i])
		}
		if !strings.Contains(e.Key, "/") && i != 2 {
			// only top.jpg should be at root; others nested
			t.Errorf("entry[%d] unexpectedly flat: %q", i, e.Key)
		}
	}
}

func TestLocalSource_List_Empty(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	entries, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestLocalSource_PutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	payload := []byte("hello world")

	if err := src.Put(context.Background(), "round/trip.jpg", bytes.NewReader(payload), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, meta, err := src.Get(context.Background(), "round/trip.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes = %q, want %q", got, payload)
	}
	if meta.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg", meta.ContentType)
	}
	if meta.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", meta.Size, len(payload))
	}
	if meta.ETag == "" {
		t.Errorf("ETag should not be empty")
	}
}

func TestLocalSource_Put_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	if err := src.Put(context.Background(), "deeply/nested/file.png", bytes.NewReader([]byte("x")), "image/png"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	p := filepath.Join(dir, "deeply", "nested", "file.png")
	if _, err := os.Stat(p); err != nil {
		t.Errorf("stat created file: %v", err)
	}
}

func TestLocalSource_Put_AtomicReplace_NoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	ctx := context.Background()

	if err := src.Put(ctx, "x.jpg", bytes.NewReader([]byte("v1")), "image/jpeg"); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := src.Put(ctx, "x.jpg", bytes.NewReader([]byte("v2-longer")), "image/jpeg"); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	// Verify content is v2.
	rc, _, err := src.Get(ctx, "x.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "v2-longer" {
		t.Errorf("content = %q, want v2-longer", got)
	}

	// Verify no .tmp.* files remain in the directory.
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp.*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temp files remain: %v", matches)
	}
}

func TestLocalSource_Get_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	_, _, err := src.Get(context.Background(), "../etc/passwd")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("error = %v, want ErrInvalidKey", err)
	}
}

func TestLocalSource_Get_MissingKey(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	_, _, err := src.Get(context.Background(), "missing.jpg")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestLocalSource_Delete_Missing(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	err := src.Delete(context.Background(), "missing.jpg")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestLocalSource_Delete_Existing(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	ctx := context.Background()

	if err := src.Put(ctx, "a.jpg", bytes.NewReader([]byte("x")), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := src.Delete(ctx, "a.jpg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.jpg")); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete: err=%v", err)
	}
	entries, _ := src.List(ctx)
	for _, e := range entries {
		if e.Key == "a.jpg" {
			t.Errorf("List still returns deleted key")
		}
	}
}

func TestLocalSource_Exists(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	ctx := context.Background()

	if err := src.Put(ctx, "here.jpg", bytes.NewReader([]byte("x")), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, err := src.Exists(ctx, "here.jpg")
	if err != nil || !ok {
		t.Errorf("Exists(here.jpg) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = src.Exists(ctx, "missing.jpg")
	if err != nil || ok {
		t.Errorf("Exists(missing.jpg) = (%v, %v), want (false, nil)", ok, err)
	}
	// Missing parent dir — still just (false, nil).
	ok, err = src.Exists(ctx, "missing/parent/file.jpg")
	if err != nil || ok {
		t.Errorf("Exists(missing/parent/file.jpg) = (%v, %v), want (false, nil)", ok, err)
	}
	// Invalid key path.
	_, err = src.Exists(ctx, "../escape.jpg")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Exists with traversal: err = %v, want ErrInvalidKey", err)
	}
}

func TestLocalSource_ETagChangesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	src, _ := NewLocalSource(dir)
	ctx := context.Background()

	if err := src.Put(ctx, "etag.jpg", bytes.NewReader([]byte("v1")), "image/jpeg"); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	entries1, _ := src.List(ctx)
	if len(entries1) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries1))
	}
	tag1 := entries1[0].ETag

	// Sleep enough that mtime nano-resolution definitely advances even on
	// filesystems with coarse mtime granularity. 20ms is plenty for NTFS
	// (100ns) and ext4 (ns), and well under typical Windows FAT 2s. We
	// also vary content length to force a size-different ETag on FATs.
	time.Sleep(20 * time.Millisecond)

	if err := src.Put(ctx, "etag.jpg", bytes.NewReader([]byte("v2-longer")), "image/jpeg"); err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	entries2, _ := src.List(ctx)
	if len(entries2) != 1 {
		t.Fatalf("expected 1 entry after replace, got %d", len(entries2))
	}
	tag2 := entries2[0].ETag

	if tag1 == tag2 {
		t.Errorf("ETag did not change after content rewrite: %q == %q", tag1, tag2)
	}
}
