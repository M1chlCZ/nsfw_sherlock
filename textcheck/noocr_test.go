//go:build !ocr

package textcheck

import "testing"

func TestEnabledWithoutOCR(t *testing.T) {
	if Enabled() {
		t.Fatal("Enabled() = true, want false without the ocr build tag")
	}
}

func TestWarmupWithoutOCR(t *testing.T) {
	if err := Warmup(); err != nil {
		t.Fatalf("Warmup() error = %v, want nil without the ocr build tag", err)
	}
}

func TestDetectTextWithoutOCR(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("fuck")} {
		got, err := DetectText(data)
		if err != nil {
			t.Errorf("DetectText(%q) error = %v, want nil", data, err)
		}
		if got {
			t.Errorf("DetectText(%q) = true, want false", data)
		}
	}
}
