package utils

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodePayloadRawBase64(t *testing.T) {
	want := []byte("hello world")
	got, err := DecodePayload(base64.StdEncoding.EncodeToString(want))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("DecodePayload = %q, want %q", got, want)
	}
}

func TestDecodePayloadDataURLPrefixes(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0}

	tests := []struct {
		name  string
		input string
		want  []byte
	}{
		{"png", "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), png},
		{"jpeg", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg), jpeg},
		{"webp", "data:image/webp;base64," + base64.StdEncoding.EncodeToString(png), png},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodePayload(tt.input)
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("DecodePayload = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodePayloadDataURLPrefixIsCaseInsensitive(t *testing.T) {
	want := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(want)

	inputs := []string{
		"DATA:image/png;base64," + encoded,
		"data:IMAGE/PNG;BASE64," + encoded,
		"DaTa:image/jpeg;BaSe64," + encoded,
	}
	for _, input := range inputs {
		got, err := DecodePayload(input)
		if err != nil {
			t.Errorf("DecodePayload(%q): %v", input, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("DecodePayload(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecodePayloadMarkerAfterHeaderIsNotStripped(t *testing.T) {
	input := "data:text/plain,AAAA;base64,BBBB"
	if _, err := DecodePayload(input); err == nil {
		t.Errorf("DecodePayload(%q) = nil error, want error", input)
	}
}

func TestDecodePayloadUnpadded(t *testing.T) {
	want := []byte("hello world")
	got, err := DecodePayload(base64.RawStdEncoding.EncodeToString(want))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("DecodePayload = %q, want %q", got, want)
	}
}

func TestDecodePayloadTrimsWhitespaceAroundInput(t *testing.T) {
	want := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(want)
	inputs := []string{
		"  " + encoded + "  ",
		"\n\t" + encoded + "\n",
		" \n data:image/png;base64," + encoded + " \n",
	}
	for _, input := range inputs {
		got, err := DecodePayload(input)
		if err != nil {
			t.Errorf("DecodePayload(%q): %v", input, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("DecodePayload(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecodePayloadRejectsEmpty(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t", "data:image/png;base64,", "data:image/png;base64,  "} {
		if _, err := DecodePayload(input); err == nil {
			t.Errorf("DecodePayload(%q) = nil error, want error", input)
		}
	}
}

func TestDecodePayloadRejectsInvalidChars(t *testing.T) {
	for _, input := range []string{"!!!!", "not base64!", "data:image/png;base64,!!!!"} {
		if _, err := DecodePayload(input); err == nil {
			t.Errorf("DecodePayload(%q) = nil error, want error", input)
		}
	}
}

func TestGenerateSecureTokenRoundTrip(t *testing.T) {
	token := GenerateSecureToken(32)
	if len(token) != 64 {
		t.Fatalf("GenerateSecureToken(32) length = %d, want 64", len(token))
	}
	decoded, err := DecodePayload(base64.StdEncoding.EncodeToString([]byte(token)))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if string(decoded) != token {
		t.Errorf("round trip = %q, want %q", decoded, token)
	}
}

func TestGenerateSecureTokenNonPositiveLength(t *testing.T) {
	for _, length := range []int{0, -1, -16} {
		if got := GenerateSecureToken(length); got != "" {
			t.Errorf("GenerateSecureToken(%d) = %q, want %q", length, got, "")
		}
	}
}

func TestGenerateSecureTokenIsHex(t *testing.T) {
	token := GenerateSecureToken(16)
	if len(token) != 32 {
		t.Fatalf("GenerateSecureToken(16) length = %d, want 32", len(token))
	}
	for _, r := range token {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("GenerateSecureToken(16) = %q, contains non-hex rune %q", token, r)
		}
	}
}
