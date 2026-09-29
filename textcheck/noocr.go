//go:build !ocr

package textcheck

// Enabled reports whether OCR-based text detection is available.
func Enabled() bool { return false }

// Warmup is a no-op when OCR is not compiled in.
func Warmup() error { return nil }

// DetectText is a no-op when OCR is not compiled in.
func DetectText(_ []byte) (bool, error) {
	return false, nil
}
