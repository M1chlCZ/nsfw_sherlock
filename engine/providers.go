package engine

import (
	"fmt"
	"math"
	"strconv"

	ort "github.com/yalue/onnxruntime_go"
)

func newSessionOptions(provider string, deviceID int) (*ort.SessionOptions, error) {
	if deviceID < 0 || deviceID > math.MaxInt32 {
		return nil, fmt.Errorf("engine: GPU device ID must be in 0..%d", math.MaxInt32)
	}
	switch provider {
	case "", "cpu":
		return nil, nil //nolint:nilnil // A nil session option selects ORT CPU defaults.
	case "cuda", "migraphx":
	default:
		return nil, fmt.Errorf("engine: unknown execution provider %q", provider)
	}
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("engine: create %s session options: %w", provider, err)
	}
	if provider == "cuda" {
		err = appendCUDA(options, deviceID)
	} else {
		err = options.AppendExecutionProvider("MIGraphX", map[string]string{"device_id": strconv.Itoa(deviceID)})
	}
	if err != nil {
		_ = options.Destroy()
		return nil, fmt.Errorf(
			"engine: enable %s device %d (requires a compatible GPU-enabled ORT_LIB): %w",
			provider,
			deviceID,
			err,
		)
	}
	return options, nil
}

func appendCUDA(options *ort.SessionOptions, deviceID int) error {
	cuda, err := ort.NewCUDAProviderOptions()
	if err != nil {
		return err
	}
	defer func() { _ = cuda.Destroy() }()
	if err := cuda.Update(map[string]string{"device_id": strconv.Itoa(deviceID)}); err != nil {
		return err
	}
	return options.AppendExecutionProviderCUDA(cuda)
}
