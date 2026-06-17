package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// existsFromSet returns an exists-callback that reports membership in
// the given set.
func existsFromSet(taken map[string]bool) func(context.Context, string) (bool, error) {
	return func(_ context.Context, key string) (bool, error) {
		return taken[key], nil
	}
}

func TestNextCopyKey_BaseAvailable(t *testing.T) {
	got, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited",
		existsFromSet(map[string]bool{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a/b-edited.jpg" {
		t.Errorf("got %q, want %q", got, "a/b-edited.jpg")
	}
}

func TestNextCopyKey_FirstCollision(t *testing.T) {
	taken := map[string]bool{"a/b-edited.jpg": true}
	got, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited",
		existsFromSet(taken))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a/b-edited-1.jpg" {
		t.Errorf("got %q, want %q", got, "a/b-edited-1.jpg")
	}
}

func TestNextCopyKey_FourthFree(t *testing.T) {
	taken := map[string]bool{
		"a/b-edited.jpg":   true,
		"a/b-edited-1.jpg": true,
		"a/b-edited-2.jpg": true,
		"a/b-edited-3.jpg": true,
	}
	got, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited",
		existsFromSet(taken))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a/b-edited-4.jpg" {
		t.Errorf("got %q, want %q", got, "a/b-edited-4.jpg")
	}
}

func TestNextCopyKey_AllExhausted(t *testing.T) {
	exists := func(_ context.Context, _ string) (bool, error) { return true, nil }
	_, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited", exists)
	if err == nil {
		t.Fatalf("expected error when all candidates taken, got nil")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error %q does not mention exhaustion", err.Error())
	}
}

func TestNextCopyKey_WebpToPng(t *testing.T) {
	got, err := NextCopyKey(context.Background(), "vacation/beach.webp", ".png", "-edited",
		existsFromSet(map[string]bool{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vacation/beach-edited.png" {
		t.Errorf("got %q, want %q", got, "vacation/beach-edited.png")
	}
}

func TestNextCopyKey_WebpToPng_Collision(t *testing.T) {
	taken := map[string]bool{"vacation/beach-edited.png": true}
	got, err := NextCopyKey(context.Background(), "vacation/beach.webp", ".png", "-edited",
		existsFromSet(taken))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vacation/beach-edited-1.png" {
		t.Errorf("got %q, want %q", got, "vacation/beach-edited-1.png")
	}
}

func TestNextCopyKey_ExistsErrorPropagated(t *testing.T) {
	sentinel := errors.New("boom")
	exists := func(_ context.Context, _ string) (bool, error) { return false, sentinel }
	_, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited", exists)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want sentinel %v", err, sentinel)
	}
}

func TestNextCopyKey_ExistsErrorOnLaterIteration(t *testing.T) {
	sentinel := errors.New("late boom")
	calls := 0
	exists := func(_ context.Context, key string) (bool, error) {
		calls++
		if key == "a/b-edited.jpg" {
			return true, nil
		}
		return false, sentinel
	}
	_, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited", exists)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want sentinel %v", err, sentinel)
	}
}

func TestNextCopyKey_CustomSuffix(t *testing.T) {
	got, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "_v2",
		existsFromSet(map[string]bool{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a/b_v2.jpg" {
		t.Errorf("got %q, want %q", got, "a/b_v2.jpg")
	}

	taken := map[string]bool{"a/b_v2.jpg": true}
	got, err = NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "_v2",
		existsFromSet(taken))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a/b_v2-1.jpg" {
		t.Errorf("got %q, want %q", got, "a/b_v2-1.jpg")
	}
}

// TestNextCopyKey_BoundaryCheck verifies the loop runs through 9999.
// We make every candidate up to 9999 taken and check the error fires
// rather than silently looping forever.
func TestNextCopyKey_BoundaryCheck(t *testing.T) {
	taken := map[string]bool{"a/b-edited.jpg": true}
	for i := 1; i <= maxCopyKeyAttempts; i++ {
		taken[fmt.Sprintf("a/b-edited-%d.jpg", i)] = true
	}
	_, err := NextCopyKey(context.Background(), "a/b.jpg", ".jpg", "-edited",
		existsFromSet(taken))
	if err == nil {
		t.Fatalf("expected exhaustion error")
	}
}
