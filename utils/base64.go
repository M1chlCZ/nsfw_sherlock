package utils

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const base64Marker = ";base64"

// DecodePayload decodes a base64 payload into bytes.
func DecodePayload(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = stripDataURLPrefix(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty base64 payload")
	}
	if strings.HasSuffix(s, "=") {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 payload: %w", err)
		}
		return decoded, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 payload: %w", err)
	}
	return decoded, nil
}

func stripDataURLPrefix(s string) string {
	if !hasASCIIPrefixFold(s, "data:") {
		return s
	}
	before, after, ok := strings.Cut(s, ",")
	if !ok {
		return s
	}
	header := before
	if len(header) < len(base64Marker) || !strings.EqualFold(header[len(header)-len(base64Marker):], base64Marker) {
		return s
	}
	return after
}

func asciiLower(s string) string {
	b := []byte(s)
	for i := range b {
		if 'A' <= b[i] && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func hasASCIIPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return asciiLower(s[:len(prefix)]) == prefix
}
