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
	case "cuda", "migraphx", "tensorrt", "openvino":
	default:
		return nil, fmt.Errorf("engine: unknown execution provider %q", provider)
	}
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("engine: create %s session options: %w", provider, err)
	}
	switch provider {
	case "cuda":
		err = appendCUDA(options, deviceID)
	case "tensorrt":
		err = appendTensorRT(options, deviceID)
	case "openvino":
		err = options.AppendExecutionProviderOpenVINO(map[string]string{"device_id": strconv.Itoa(deviceID)})
	default:
		err = options.AppendExecutionProvider("MIGraphX", map[string]string{"device_id": strconv.Itoa(deviceID)})
	}
	if err != nil {
		_ = options.Destroy()
		return nil, fmt.Errorf(
			"engine: enable %s device %d (requires an ORT_LIB with the %s execution provider): %w",
			provider,
			deviceID,
			provider,
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

func appendTensorRT(options *ort.SessionOptions, deviceID int) error {
	trt, err := ort.NewTensorRTProviderOptions()
	if err != nil {
		return err
	}
	defer func() { _ = trt.Destroy() }()
	if err := trt.Update(map[string]string{"device_id": strconv.Itoa(deviceID)}); err != nil {
		return err
	}
	return options.AppendExecutionProviderTensorRT(trt)
}
