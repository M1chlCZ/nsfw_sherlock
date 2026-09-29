package utils

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateSecureToken returns a cryptographically random hex string.
func GenerateSecureToken(length int) string {
	if length <= 0 {
		return ""
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
