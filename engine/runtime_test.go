package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	testMissingLibPath = "/nonexistent/libonnxruntime.dylib"
	runWaitTimeout     = 5 * time.Second
)

type fakeRunner struct {
	run     func(inputs, outputs []ort.Value) error
	destroy func() error
}

func (f *fakeRunner) Run(inputs, outputs []ort.Value) error {
	if f.run == nil {
		return nil
	}
	return f.run(inputs, outputs)
}

func (f *fakeRunner) Destroy() error {
	if f.destroy == nil {
		return nil
	}
	return f.destroy()
}

func localORTLibPath() string {
	name := ortLibName()
	if name == "" {
		return ""
	}
	for _, dir := range []string{"lib", filepath.Join("..", "lib")} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func waitStart(t *testing.T, entered <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(runWaitTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitError(t *testing.T, results <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(runWaitTimeout):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func stubLib(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	path := filepath.Join(dir, "lib", ortLibName())
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write stub lib: %v", err)
	}
	return path
}

func stubExecutable(t *testing.T, dir string) {
	t.Helper()
	old := executablePath
	executablePath = func() (string, error) { return filepath.Join(dir, "engine.test"), nil }
	t.Cleanup(func() { executablePath = old })
}

func TestResolveLibPath(t *testing.T) {
	name := ortLibName()
	if name == "" {
		t.Skip("unsupported OS")
	}

	t.Run("ORT_LIB wins even when missing", func(t *testing.T) {
		want := "/custom/ort/" + name
		t.Setenv("ORT_LIB", want)
		t.Chdir(t.TempDir())
		stubExecutable(t, t.TempDir())
		if got := resolveLibPath(); got != want {
			t.Fatalf("resolveLibPath() = %q, want %q", got, want)
		}
	})

	t.Run("working directory candidate wins", func(t *testing.T) {
		t.Setenv("ORT_LIB", "")
		dir := t.TempDir()
		stubLib(t, dir)
		want := filepath.Join("lib", name)
		exeDir := t.TempDir()
		stubLib(t, exeDir)
		t.Chdir(dir)
		stubExecutable(t, exeDir)
		if got := resolveLibPath(); got != want {
			t.Fatalf("resolveLibPath() = %q, want %q", got, want)
		}
	})

	t.Run("executable directory candidate", func(t *testing.T) {
		t.Setenv("ORT_LIB", "")
		t.Chdir(t.TempDir())
		exeDir := t.TempDir()
		want := stubLib(t, exeDir)
		stubExecutable(t, exeDir)
		if got := resolveLibPath(); got != want {
			t.Fatalf("resolveLibPath() = %q, want %q", got, want)
		}
	})

	t.Run("candidate order", func(t *testing.T) {
		t.Setenv("ORT_LIB", "")
		exeDir := t.TempDir()
		stubExecutable(t, exeDir)
		want := []string{filepath.Join("lib", name), filepath.Join(exeDir, "lib", name)}
		if got := libCandidates(); !slices.Equal(got, want) {
			t.Fatalf("libCandidates() = %q, want %q", got, want)
		}
	})

	t.Run("missing lib", func(t *testing.T) {
		t.Setenv("ORT_LIB", "")
		t.Chdir(t.TempDir())
		stubExecutable(t, t.TempDir())
		if got := resolveLibPath(); got != "" {
			t.Fatalf("resolveLibPath() = %q, want empty", got)
		}
	})

	t.Run("only candidates for the current OS", func(t *testing.T) {
		t.Setenv("ORT_LIB", "")
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
			t.Fatalf("mkdir lib: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lib", "libonnxruntime.other"), []byte("stub"), 0o644); err != nil {
			t.Fatalf("write stub lib: %v", err)
		}
		t.Chdir(dir)
		stubExecutable(t, t.TempDir())
		if got := resolveLibPath(); got != "" {
			t.Fatalf("resolveLibPath() = %q, want empty", got)
		}
	})
}

func TestInitMissingLib(t *testing.T) {
	if os.Getenv("ENGINE_TEST_MISSING_LIB") == "1" {
		err := InitRuntime(testMissingLibPath)
		if err == nil {
			fmt.Fprintln(os.Stderr, "InitRuntime with a missing library returned nil")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, err)
		os.Exit(0)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestInitMissingLib$")
	cmd.Env = append(os.Environ(), "ENGINE_TEST_MISSING_LIB=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), testMissingLibPath) {
		t.Fatalf("subprocess output does not mention %q:\n%s", testMissingLibPath, out)
	}
}

func TestModelPoolConcurrency(t *testing.T) {
	var current, peak int32
	entered := make(chan struct{}, 6)
	proceed := make(chan struct{})

	factory := func() (modelRunner, error) {
		return &fakeRunner{
			run: func(inputs, outputs []ort.Value) error {
				n := atomic.AddInt32(&current, 1)
				for {
					old := atomic.LoadInt32(&peak)
					if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
						break
					}
				}
				entered <- struct{}{}
				<-proceed
				atomic.AddInt32(&current, -1)
				return nil
			},
		}, nil
	}

	p, err := newModelPoolWithFactory(factory, 2)
	if err != nil {
		t.Fatalf("newModelPoolWithFactory: %v", err)
	}
	defer p.close()

	const perWave = 6
	for wave := range 2 {
		results := make(chan error, perWave)
		for range perWave {
			go func() {
				_, err := p.run(context.Background(), nil, nil)
				results <- err
			}()
		}
		for done := 0; done < perWave; done += 2 {
			waitStart(t, entered, "a run to start")
			waitStart(t, entered, "a second run to start")
			select {
			case <-entered:
				t.Fatal("more than 2 runs started concurrently")
			default:
			}
			if got := atomic.LoadInt32(&peak); got != 2 {
				t.Fatalf("wave %d: max concurrency = %d, want 2", wave, got)
			}
			proceed <- struct{}{}
			proceed <- struct{}{}
		}
		for range perWave {
			if err := waitError(t, results, "runs to finish"); err != nil {
				t.Fatalf("wave %d: run error = %v", wave, err)
			}
		}
		if got := len(p.runners); got != 2 {
			t.Fatalf("wave %d: pool holds %d sessions after wave, want 2", wave, got)
		}
	}
}

func TestNewModelPoolValidation(t *testing.T) {
	factory := func() (modelRunner, error) { return &fakeRunner{}, nil }

	if _, err := newModelPoolWithFactory(factory, 0); err == nil {
		t.Fatal("size 0: error = nil, want error")
	}
	if _, err := newModelPoolWithFactory(factory, -1); err == nil {
		t.Fatal("size -1: error = nil, want error")
	}
	if _, err := newModelPoolWithFactory(nil, 1); err == nil {
		t.Fatal("nil factory: error = nil, want error")
	}
}

func TestNewPoolPartialFactoryFailure(t *testing.T) {
	var created, destroyed int32
	factory := func() (modelRunner, error) {
		if n := atomic.AddInt32(&created, 1); n == 2 {
			return nil, errors.New("session boom")
		}
		return &fakeRunner{
			destroy: func() error {
				atomic.AddInt32(&destroyed, 1)
				return nil
			},
		}, nil
	}

	_, err := newPool("test.onnx", factory, 3, 0, nil)
	if err == nil {
		t.Fatal("newPool error = nil, want error")
	}
	if !strings.Contains(err.Error(), "test.onnx") || !strings.Contains(err.Error(), "session 2/3") {
		t.Fatalf("newPool error = %q, want model path and session index", err)
	}
	if got := atomic.LoadInt32(&created); got != 2 {
		t.Fatalf("factory calls = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&destroyed); got != 1 {
		t.Fatalf("destroyed sessions = %d, want 1", got)
	}
}

func TestModelPoolRunError(t *testing.T) {
	fakeErr := errors.New("fake inference failure")
	var calls atomic.Int32
	factory := func() (modelRunner, error) {
		return &fakeRunner{
			run: func(inputs, outputs []ort.Value) error {
				if calls.Add(1) == 1 {
					return fakeErr
				}
				return nil
			},
		}, nil
	}

	p, err := newPool("test.onnx", factory, 1, 0, nil)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.close()

	_, err = p.run(context.Background(), nil, nil)
	if !errors.Is(err, fakeErr) {
		t.Fatalf("run error = %v, want %v", err, fakeErr)
	}
	if !strings.Contains(err.Error(), "test.onnx") || !strings.Contains(err.Error(), "run:") {
		t.Fatalf("run error = %q, want model path and run context", err)
	}
	if got := len(p.runners); got != 1 {
		t.Fatalf("pool holds %d sessions after failed run, want 1", got)
	}
	if _, err := p.run(context.Background(), nil, nil); err != nil {
		t.Fatalf("run after error = %v, want nil", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("runner calls = %d, want 2", got)
	}
}

func TestModelPoolRunContext(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	proceed := make(chan struct{})
	factory := func() (modelRunner, error) {
		return &fakeRunner{
			run: func(inputs, outputs []ort.Value) error {
				calls.Add(1)
				entered <- struct{}{}
				<-proceed
				return nil
			},
		}, nil
	}

	p, err := newModelPoolWithFactory(factory, 1)
	if err != nil {
		t.Fatalf("newModelPoolWithFactory: %v", err)
	}
	defer p.close()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.run(cancelled, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled run error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("pre-cancelled run called the runner %d times, want 0", got)
	}

	first := make(chan error, 1)
	go func() {
		_, err := p.run(context.Background(), nil, nil)
		first <- err
	}()
	waitStart(t, entered, "the first run to saturate the pool")

	waiting, cancelWait := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := p.run(waiting, nil, nil)
		second <- err
	}()
	cancelWait()
	if err := waitError(t, second, "the waiting run to observe cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting run error = %v, want context.Canceled", err)
	}

	proceed <- struct{}{}
	if err := waitError(t, first, "the first run to finish"); err != nil {
		t.Fatalf("first run error = %v, want nil", err)
	}
	if got := len(p.runners); got != 1 {
		t.Fatalf("pool holds %d sessions after waits, want 1", got)
	}
}

func TestModelPoolClose(t *testing.T) {
	var destroyed atomic.Int32
	entered := make(chan struct{}, 2)
	proceed := make(chan struct{})
	factory := func() (modelRunner, error) {
		return &fakeRunner{
			run: func(inputs, outputs []ort.Value) error {
				entered <- struct{}{}
				<-proceed
				return nil
			},
			destroy: func() error {
				destroyed.Add(1)
				return nil
			},
		}, nil
	}

	p, err := newModelPoolWithFactory(factory, 2)
	if err != nil {
		t.Fatalf("newModelPoolWithFactory: %v", err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := p.run(context.Background(), nil, nil)
			results <- err
		}()
	}
	waitStart(t, entered, "the first in-flight run")
	waitStart(t, entered, "the second in-flight run")

	p.close()
	if _, err := p.run(context.Background(), nil, nil); !errors.Is(err, errPoolClosed) {
		t.Fatalf("run after close error = %v, want %v", err, errPoolClosed)
	}

	proceed <- struct{}{}
	proceed <- struct{}{}
	for range 2 {
		if err := waitError(t, results, "in-flight runs to finish"); !errors.Is(err, errPoolClosed) {
			t.Fatalf("in-flight run error = %v, want %v", err, errPoolClosed)
		}
	}
	if got := destroyed.Load(); got != 2 {
		t.Fatalf("destroyed sessions = %d, want 2", got)
	}

	p.close()
	if got := destroyed.Load(); got != 2 {
		t.Fatalf("second close destroyed sessions: destroyed = %d, want 2", got)
	}
}

func TestInitRuntimeLocalLib(t *testing.T) {
	libPath := localORTLibPath()
	if libPath == "" {
		t.Skip("no ORT library in lib/; run scripts/fetch-models.sh --ort-only")
	}

	if err := InitRuntime(libPath); err != nil {
		t.Fatalf("InitRuntime(%q) error = %v", libPath, err)
	}
	if !ort.IsInitialized() {
		t.Fatal("ort.IsInitialized() = false after InitRuntime")
	}
	if err := InitRuntime(libPath); err != nil {
		t.Fatalf("second InitRuntime(%q) error = %v", libPath, err)
	}
}
