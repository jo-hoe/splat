package source

import (
	"context"
	"fmt"
	"strings"
)

// maxCopyKeyAttempts caps the suffix counter as documented in DESIGN §2.8.
const maxCopyKeyAttempts = 9999

// NextCopyKey returns the target key for a "save as copy" operation.
//
// Given a source key like "vacation/beach.webp", an output extension
// like ".png" (which handles the WebP-becomes-PNG conversion), and a
// suffix like "-edited", NextCopyKey builds:
//
//	"vacation/beach-edited.png"
//
// and asks the supplied exists callback whether that key is taken. If
// it is, NextCopyKey tries "-edited-1.png", "-edited-2.png", ... up to
// "-edited-9999.png" before giving up with an error.
//
// Errors from exists are propagated immediately. NextCopyKey performs
// no I/O of its own.
func NextCopyKey(
	ctx context.Context,
	srcKey, outputExt, suffix string,
	exists func(context.Context, string) (bool, error),
) (string, error) {
	stem := stripExt(srcKey)
	base := stem + suffix + outputExt

	taken, err := exists(ctx, base)
	if err != nil {
		return "", err
	}
	if !taken {
		return base, nil
	}

	for i := 1; i <= maxCopyKeyAttempts; i++ {
		candidate := fmt.Sprintf("%s%s-%d%s", stem, suffix, i, outputExt)
		taken, err := exists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("source: exhausted %d copy-key candidates for %q", maxCopyKeyAttempts, srcKey)
}

// stripExt returns key with its final extension removed. A key with no
// extension is returned unchanged.
func stripExt(key string) string {
	idx := strings.LastIndex(key, ".")
	slash := strings.LastIndex(key, "/")
	if idx <= slash {
		// No extension, or the last "." is part of a directory name.
		return key
	}
	return key[:idx]
}
