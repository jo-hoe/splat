package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// sha256Hex consumes r fully and returns the hex-encoded SHA-256 of the
// bytes read alongside the bytes themselves. This is the optimistic-
// concurrency primitive used by the apply handler: read once, hash once,
// keep the bytes for decoding.
func sha256Hex(r io.Reader) (string, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}
