package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var errPoolClosed = errors.New("engine: model pool closed")

var (
	runtimeOnce sync.Once
	runtimeErr  error
)

var executablePath = os.Executable

// InitRuntime loads the ONNX Runtime shared library and initializes the process-wide ORT environment.
func InitRuntime(libPath string) error {
	runtimeOnce.Do(func() {
		path := libPath
		if path == "" {
			path = resolveLibPath()
		}
		if path == "" {
			runtimeErr = fmt.Errorf("engine: onnxruntime shared library not found; set ORT_LIB or run scripts/fetch-models.sh --ort-only")
			return
		}
		ort.SetSharedLibraryPath(path)
		if err := ort.InitializeEnvironment(); err != nil {
			runtimeErr = fmt.Errorf("engine: initialize onnxruntime %q: %w; set ORT_LIB or run scripts/fetch-models.sh --ort-only", path, err)
		}
	})
	return runtimeErr
}

func resolveLibPath() string {
	if path := os.Getenv("ORT_LIB"); path != "" {
		return path
	}
	for _, path := range libCandidates() {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func libCandidates() []string {
	name := ortLibName()
	if name == "" {
		return nil
	}
	candidates := []string{filepath.Join("lib", name)}
	if exe, err := executablePath(); err == nil && exe != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "lib", name))
	}
	return candidates
}

func ortLibName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib"
	case "linux":
		return "libonnxruntime.so"
	default:
		return ""
	}
}

type modelRunner interface {
	Run(inputs, outputs []ort.Value) error
	Destroy() error
}

type inputBuilder func(inputData []float32, shape ort.Shape) (ort.Value, error)

type modelPool struct {
	modelPath   string
	runners     chan modelRunner
	outputCount int
	buildInput  inputBuilder

	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func newModelPool(modelPath string, inputNames, outputNames []string, size int) (*modelPool, error) {
	factory := func() (modelRunner, error) {
		return ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, nil)
	}
	return newPool(modelPath, factory, size, len(outputNames), func(inputData []float32, shape ort.Shape) (ort.Value, error) {
		return ort.NewTensor(shape, inputData)
	})
}

func newModelPoolWithFactory(factory func() (modelRunner, error), size int) (*modelPool, error) {
	return newPool("", factory, size, 0, nil)
}

func newPool(modelPath string, factory func() (modelRunner, error), size, outputCount int, buildInput inputBuilder) (*modelPool, error) {
	if size < 1 {
		return nil, fmt.Errorf("engine: model %q: pool size %d must be at least 1", modelPath, size)
	}
	if factory == nil {
		return nil, fmt.Errorf("engine: model %q: nil model runner factory", modelPath)
	}
	p := &modelPool{
		modelPath:   modelPath,
		runners:     make(chan modelRunner, size),
		outputCount: outputCount,
		buildInput:  buildInput,
	}
	for i := 1; i <= size; i++ {
		runner, err := factory()
		if err != nil {
			p.close()
			return nil, fmt.Errorf("engine: model %q: create session %d/%d: %w", modelPath, i, size, err)
		}
		if runner == nil {
			p.close()
			return nil, fmt.Errorf("engine: model %q: model runner factory returned nil for session %d/%d", modelPath, i, size)
		}
		p.runners <- runner
	}
	return p, nil
}

func (p *modelPool) run(ctx context.Context, inputData []float32, shape ort.Shape) ([]ort.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var runner modelRunner
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r, ok := <-p.runners:
		if !ok {
			return nil, errPoolClosed
		}
		runner = r
	}
	if err := ctx.Err(); err != nil {
		p.release(runner)
		return nil, err
	}
	if p.isClosed() {
		p.release(runner)
		return nil, errPoolClosed
	}

	outputs, err := p.infer(runner, inputData, shape)
	p.release(runner)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		destroyValues(outputs)
		return nil, err
	}
	if p.isClosed() {
		destroyValues(outputs)
		return nil, errPoolClosed
	}
	return outputs, nil
}

func (p *modelPool) infer(runner modelRunner, inputData []float32, shape ort.Shape) ([]ort.Value, error) {
	var inputs []ort.Value
	if p.buildInput != nil {
		input, err := p.buildInput(inputData, shape)
		if err != nil {
			return nil, fmt.Errorf("engine: model %q: create input tensor: %w", p.modelPath, err)
		}
		if input != nil {
			defer func() { _ = input.Destroy() }()
			inputs = []ort.Value{input}
		}
	}

	outputs := make([]ort.Value, p.outputCount)
	if err := runner.Run(inputs, outputs); err != nil {
		destroyValues(outputs)
		return nil, fmt.Errorf("engine: model %q: run: %w", p.modelPath, err)
	}
	return outputs, nil
}

func (p *modelPool) release(runner modelRunner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = runner.Destroy()
		return
	}
	p.runners <- runner
}

func (p *modelPool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *modelPool) close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.runners)
		var idle []modelRunner
		for runner := range p.runners {
			idle = append(idle, runner)
		}
		p.mu.Unlock()
		for _, runner := range idle {
			_ = runner.Destroy()
		}
	})
}

func destroyValues(values []ort.Value) {
	for _, v := range values {
		if v != nil {
			_ = v.Destroy()
		}
	}
}
