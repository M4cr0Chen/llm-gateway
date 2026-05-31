package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashKey returns the hex-encoded SHA-256 of the plaintext key. This is
// the format persisted in api_keys.key_hash.
func HashKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
