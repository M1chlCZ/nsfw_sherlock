package engine

import (
	"strings"
	"testing"
)

func TestSessionOptionsValidation(t *testing.T) {
	for _, provider := range []string{"", "cpu"} {
		opts, err := newSessionOptions(provider, 0)
		if opts != nil || err != nil {
			t.Fatalf("CPU options = %v, %v; want nil, nil", opts, err)
		}
	}
	for _, tc := range []struct {
		provider string
		deviceID int
	}{{"unknown", 0}, {"cuda", -1}, {"migraphx", -1}, {"cpu", -1}} {
		if _, err := newSessionOptions(tc.provider, tc.deviceID); err == nil {
			t.Fatalf("accepted provider %q, device %d", tc.provider, tc.deviceID)
		}
	}
}

func TestUnavailableGPUProvider(t *testing.T) {
	if err := parityInitRuntime(); err != nil {
		t.Skipf("ONNX runtime unavailable: %v", err)
	}
	for _, provider := range []string{"cuda", "migraphx"} {
		opts, err := newSessionOptions(provider, 0)
		if err == nil {
			if closeErr := opts.Destroy(); closeErr != nil {
				t.Fatal(closeErr)
			}
			continue // A GPU runtime is installed; TestParity exercises inference.
		}
		if !strings.Contains(err.Error(), "GPU-enabled ORT_LIB") {
			t.Fatalf("%s error lacks setup guidance: %v", provider, err)
		}
	}
}
