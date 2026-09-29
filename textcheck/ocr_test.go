//go:build ocr

package textcheck

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/otiai10/gosseract/v2"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func requireTesseract(t *testing.T) {
	t.Helper()
	if err := Warmup(); err != nil {
		t.Skipf("tesseract/tessdata unavailable; skipping OCR test: %v", err)
	}
}

func rawOCR(t *testing.T, data []byte) string {
	t.Helper()

	client := gosseract.NewClient()
	defer client.Close()

	if err := client.SetLanguage("eng"); err != nil {
		t.Fatalf("cannot set OCR language: %v", err)
	}
	if err := client.SetImageFromBytes(data); err != nil {
		t.Fatalf("cannot load OCR image: %v", err)
	}
	text, err := client.Text()
	if err != nil {
		t.Fatalf("OCR failed: %v", err)
	}
	return text
}

func wordImage(t *testing.T, word string) []byte {
	t.Helper()

	const (
		pad   = 8
		scale = 8
	)

	face := basicfont.Face7x13
	metrics := face.Metrics()

	width := font.MeasureString(face, word).Ceil() + 2*pad
	height := metrics.Height.Ceil() + 2*pad

	small := image.NewGray(image.Rect(0, 0, width, height))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	drawer := &font.Drawer{
		Dst:  small,
		Src:  image.NewUniform(color.Black),
		Face: face,
		Dot:  fixed.P(pad, pad+metrics.Ascent.Ceil()),
	}
	drawer.DrawString(word)

	large := image.NewGray(image.Rect(0, 0, width*scale, height*scale))
	xdraw.NearestNeighbor.Scale(large, large.Bounds(), small, small.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, large); err != nil {
		t.Fatalf("cannot encode test image: %v", err)
	}
	return buf.Bytes()
}

func TestEnabledWithOCR(t *testing.T) {
	if !Enabled() {
		t.Fatal("Enabled() = false, want true with the ocr build tag")
	}
}

func TestWarmupWithOCR(t *testing.T) {
	requireTesseract(t)

	if err := Warmup(); err != nil {
		t.Fatalf("second Warmup() error = %v, want nil", err)
	}
}

func TestDetectTextWithOCR(t *testing.T) {
	requireTesseract(t)
	loadEmbedded(t)

	badImage := wordImage(t, "fuck")
	if text := rawOCR(t, badImage); !strings.Contains(strings.ToLower(text), "fuck") {
		t.Fatalf("OCR did not read the rendered bad word; output = %q", text)
	}
	got, err := DetectText(badImage)
	if err != nil {
		t.Fatalf("DetectText(bad word image) error = %v, want nil", err)
	}
	if !got {
		t.Errorf("DetectText(bad word image) = false, want true")
	}

	safeImage := wordImage(t, "hello")
	got, err = DetectText(safeImage)
	if err != nil {
		t.Fatalf("DetectText(safe word image) error = %v, want nil", err)
	}
	if got {
		t.Errorf("DetectText(safe word image) = true, want false")
	}
}
