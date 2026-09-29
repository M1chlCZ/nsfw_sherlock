//go:build ocr

package textcheck

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sync"

	"github.com/otiai10/gosseract/v2"
)

var warmupImage = sync.OnceValues(func() ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
})

// Enabled reports whether OCR-based text detection is available.
func Enabled() bool { return true }

// Warmup initializes a Tesseract client and runs OCR on a tiny blank image to force tessdata initialization.
func Warmup() error {
	client := gosseract.NewClient()
	defer client.Close()

	if err := client.SetLanguage("eng"); err != nil {
		return fmt.Errorf("textcheck: OCR warmup: cannot set language: %w", err)
	}
	data, err := warmupImage()
	if err != nil {
		return fmt.Errorf("textcheck: OCR warmup: cannot render blank image: %w", err)
	}
	if err := client.SetImageFromBytes(data); err != nil {
		return fmt.Errorf("textcheck: OCR warmup: cannot load blank image: %w", err)
	}
	if _, err := client.Text(); err != nil {
		return fmt.Errorf("textcheck: OCR warmup failed: %w", err)
	}
	return nil
}

// DetectText runs OCR (Tesseract, English) over the encoded image in data and reports whether the recognized text contains a bad word.
func DetectText(data []byte) (bool, error) {
	client := gosseract.NewClient()
	defer client.Close()

	if err := client.SetLanguage("eng"); err != nil {
		return false, fmt.Errorf("textcheck: cannot set OCR language: %w", err)
	}
	if err := client.SetImageFromBytes(data); err != nil {
		return false, fmt.Errorf("textcheck: cannot load image data for OCR: %w", err)
	}

	text, err := client.Text()
	if err != nil {
		return false, fmt.Errorf("textcheck: OCR failed: %w", err)
	}
	if len(text) == 0 {
		return false, nil
	}
	return len(ContainsBadWords(text)) > 0, nil
}
