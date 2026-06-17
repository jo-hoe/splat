package source

import (
	"errors"
	"testing"
)

func TestCleanKey_HappyPaths(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a.jpg", "a.jpg"},
		{"nested/b.png", "nested/b.png"},
		{"Photo.JPG", "Photo.jpg"},
		{"Mixed/Camel-Case.JpEg", "Mixed/Camel-Case.jpeg"},
		{"deep/nested/path/file.webp", "deep/nested/path/file.webp"},
	}
	for _, c := range cases {
		got, err := CleanKey(c.in)
		if err != nil {
			t.Errorf("CleanKey(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CleanKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanKey_Rejections(t *testing.T) {
	bad := []string{
		"",
		"/leading.jpg",
		"../escape.jpg",
		"a/../b.jpg",
		"a/./b.jpg",
		".",
		"..",
		"a//b.jpg",
		"back\\slash.jpg",
		"mixed\\inside/path.jpg",
	}
	for _, in := range bad {
		_, err := CleanKey(in)
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("CleanKey(%q) error = %v, want ErrInvalidKey", in, err)
		}
	}
}

func TestIsAllowedExt(t *testing.T) {
	good := []string{".jpg", ".JPG", ".jpeg", ".JpEg", ".png", ".PNG", ".webp", ".WebP"}
	for _, ext := range good {
		if !IsAllowedExt(ext) {
			t.Errorf("IsAllowedExt(%q) = false, want true", ext)
		}
	}
	bad := []string{"", ".gif", ".bmp", ".tiff", ".jpgx", "jpg", "."}
	for _, ext := range bad {
		if IsAllowedExt(ext) {
			t.Errorf("IsAllowedExt(%q) = true, want false", ext)
		}
	}
}
