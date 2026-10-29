package engine

import (
	"bytes"
	"image/png"
	"testing"
)

func TestWarmUpImageIsAOnePixelPNG(t *testing.T) {
	decoded, err := png.Decode(bytes.NewReader(warmUpImage))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != 1 || bounds.Dy() != 1 {
		t.Fatalf("size = %dx%d, want 1x1", bounds.Dx(), bounds.Dy())
	}
}
