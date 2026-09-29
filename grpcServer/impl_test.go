package grpcserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"
	"nsfw_sherlock/grpcModels"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const testPayload = "image-bytes"

type fakeAnalyzer struct {
	analysis engine.Analysis
	err      error
	fn       func(ctx context.Context, data []byte) (engine.Analysis, error)

	mu    sync.Mutex
	calls int
	data  []byte
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, data []byte) (engine.Analysis, error) {
	f.mu.Lock()
	f.calls++
	f.data = append([]byte(nil), data...)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, data)
	}
	return f.analysis, f.err
}

func (f *fakeAnalyzer) snapshot() (int, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]byte(nil), f.data...)
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func override[T any](t *testing.T, target *T, value T) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

func encodePayload(t *testing.T) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(testPayload))
}

func newTestClient(t *testing.T, analyzer Analyzer, checker TextChecker) grpcModels.NSFWClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(unaryInterceptors(testLogger())...))
	grpcModels.RegisterNSFWServer(server, NewServer(analyzer, checker, testLogger()))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return grpcModels.NewNSFWClient(conn)
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("status.Code(%v) = %v, want %v", err, got, want)
	}
}

func TestNewServerNilAnalyzerPanics(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("NewServer(nil, ...) did not panic")
		}
		if message := fmt.Sprint(recovered); !strings.Contains(message, "nil analyzer") {
			t.Fatalf("panic = %q, want message containing %q", message, "nil analyzer")
		}
	}()
	_ = NewServer(nil, nil, testLogger())
}

func TestDetectCheckerTimeout(t *testing.T) {
	override(t, &ocrTimeout, 20*time.Millisecond)
	payload := encodePayload(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	checker := func(context.Context, []byte) (bool, error) {
		<-release
		return false, nil
	}
	client := newTestClient(t, &fakeAnalyzer{}, checker)

	_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.DeadlineExceeded)
}

func TestDetectAnalyzerPanic(t *testing.T) {
	payload := encodePayload(t)
	analyzer := &fakeAnalyzer{fn: func(context.Context, []byte) (engine.Analysis, error) {
		panic("analyzer exploded")
	}}
	client := newTestClient(t, analyzer, nil)

	_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.Internal)
}

func TestDetectCheckerPanic(t *testing.T) {
	payload := encodePayload(t)
	checker := func(context.Context, []byte) (bool, error) { panic("checker exploded") }
	client := newTestClient(t, &fakeAnalyzer{}, checker)

	_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.Internal)
}

func TestDetect(t *testing.T) {
	payload := encodePayload(t)

	t.Run("nsfwPicture and nsfwText", func(t *testing.T) {
		analyzer := &fakeAnalyzer{analysis: engine.Analysis{Labels: engine.Labels{Porn: 0.9}}}
		checker := func(context.Context, []byte) (bool, error) { return true, nil }
		client := newTestClient(t, analyzer, checker)

		resp, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
		if !resp.GetNsfwPicture() {
			t.Fatalf("NsfwPicture = false, want true")
		}
		if !resp.GetNsfwText() {
			t.Fatalf("NsfwText = false, want true")
		}
		calls, data := analyzer.snapshot()
		if calls != 1 {
			t.Fatalf("analyzer calls = %d, want 1", calls)
		}
		if string(data) != testPayload {
			t.Fatalf("analyzer data = %q, want %q", data, testPayload)
		}
	})

	t.Run("safe picture and text", func(t *testing.T) {
		checker := func(context.Context, []byte) (bool, error) { return false, nil }
		client := newTestClient(t, &fakeAnalyzer{}, checker)

		resp, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
		if resp.GetNsfwPicture() {
			t.Fatalf("NsfwPicture = true, want false")
		}
		if resp.GetNsfwText() {
			t.Fatalf("NsfwText = true, want false")
		}
	})

	t.Run("nil checker", func(t *testing.T) {
		client := newTestClient(t, &fakeAnalyzer{}, nil)

		resp, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
		if resp.GetNsfwText() {
			t.Fatalf("NsfwText = true, want false")
		}
	})

	t.Run("explicit detection forces nsfwPicture", func(t *testing.T) {
		analysis := engine.Combine("balanced", nil,
			[]engine.Detection{{Label: "porn", Score: 0.9}},
			engine.DetectionRules{Explicit: []string{"porn"}})
		if !analysis.LegacyNSFW() {
			t.Fatalf("LegacyNSFW() = false, want true")
		}
		client := newTestClient(t, &fakeAnalyzer{analysis: analysis}, nil)

		resp, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
		if !resp.GetNsfwPicture() {
			t.Fatalf("NsfwPicture = false, want true")
		}
	})

	t.Run("unrelated detection does not force nsfwPicture", func(t *testing.T) {
		analysis := engine.Combine("balanced", nil,
			[]engine.Detection{{Label: "x", Score: 0.9}},
			engine.DetectionRules{Explicit: []string{"porn"}})
		client := newTestClient(t, &fakeAnalyzer{analysis: analysis}, nil)

		resp, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
		if resp.GetNsfwPicture() {
			t.Fatalf("NsfwPicture = true, want false")
		}
	})
}

func TestDetectInvalidPayload(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	client := newTestClient(t, analyzer, nil)

	for _, name := range []string{"empty", "bad base64"} {
		t.Run(name, func(t *testing.T) {
			payload := ""
			if name == "bad base64" {
				payload = "not base64!!"
			}
			_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
			assertCode(t, err, codes.InvalidArgument)
		})
	}

	if calls, _ := analyzer.snapshot(); calls != 0 {
		t.Fatalf("analyzer calls = %d, want 0", calls)
	}
}

func TestDetectAnalyzerErrors(t *testing.T) {
	payload := encodePayload(t)
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"too large", fmt.Errorf("engine: %w", engine.ErrImageTooLarge), codes.ResourceExhausted},
		{"unsupported", fmt.Errorf("engine: %w", engine.ErrUnsupportedImage), codes.InvalidArgument},
		{"deadline", fmt.Errorf("engine: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"canceled", fmt.Errorf("engine: %w", context.Canceled), codes.Canceled},
		{"generic", errors.New("boom"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, &fakeAnalyzer{err: tc.err}, nil)
			_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
			assertCode(t, err, tc.want)
		})
	}
}

func TestDetectCheckerErrors(t *testing.T) {
	payload := encodePayload(t)
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"generic", errors.New("ocr down"), codes.Internal},
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checker := func(context.Context, []byte) (bool, error) { return false, tc.err }
			client := newTestClient(t, &fakeAnalyzer{}, checker)

			_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
			assertCode(t, err, tc.want)
		})
	}
}

func TestDetectLabelsCheckerError(t *testing.T) {
	payload := encodePayload(t)
	checker := func(context.Context, []byte) (bool, error) { return false, errors.New("ocr down") }
	client := newTestClient(t, &fakeAnalyzer{}, checker)

	_, err := client.DetectLabels(context.Background(), &grpcModels.NSFWLabelsRequest{Base64: payload})
	assertCode(t, err, codes.Internal)
}

func TestAnalyzeCheckerError(t *testing.T) {
	payload := encodePayload(t)
	checker := func(context.Context, []byte) (bool, error) { return false, errors.New("ocr down") }
	client := newTestClient(t, &fakeAnalyzer{}, checker)

	_, err := client.Analyze(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.Internal)
}

func TestDetectLabels(t *testing.T) {
	payload := encodePayload(t)
	analysis := engine.Analysis{Labels: engine.Labels{
		Drawings: 0.1,
		Hentai:   0.2,
		Neutral:  0.3,
		Porn:     0.4,
		Sexy:     0.5,
	}}
	analyzer := &fakeAnalyzer{analysis: analysis}
	checker := func(context.Context, []byte) (bool, error) { return true, nil }
	client := newTestClient(t, analyzer, checker)

	resp, err := client.DetectLabels(context.Background(), &grpcModels.NSFWLabelsRequest{Base64: payload})
	if err != nil {
		t.Fatalf("DetectLabels() error = %v", err)
	}
	want := &grpcModels.NSFWLabels{
		Drawings: 0.1,
		Hentai:   0.2,
		Neutral:  0.3,
		Porn:     0.4,
		Sexy:     0.5,
		NsfwText: true,
	}
	if !proto.Equal(resp, want) {
		t.Fatalf("DetectLabels() = %v, want %v", resp, want)
	}
	calls, data := analyzer.snapshot()
	if calls != 1 || string(data) != testPayload {
		t.Fatalf("analyzer calls = %d, data = %q, want 1, %q", calls, data, testPayload)
	}
}

func TestDetectLabelsErrors(t *testing.T) {
	payload := encodePayload(t)

	_, err := newTestClient(t, &fakeAnalyzer{err: engine.ErrUnsupportedImage}, nil).DetectLabels(
		context.Background(), &grpcModels.NSFWLabelsRequest{Base64: payload})
	assertCode(t, err, codes.InvalidArgument)

	_, err = newTestClient(t, &fakeAnalyzer{}, nil).DetectLabels(
		context.Background(), &grpcModels.NSFWLabelsRequest{})
	assertCode(t, err, codes.InvalidArgument)
}

func TestAnalyze(t *testing.T) {
	payload := encodePayload(t)
	analysis := engine.Analysis{
		Verdict: engine.VerdictExplicit,
		NSFW:    0.9,
		Labels: engine.Labels{
			Drawings: 0.1,
			Hentai:   0.2,
			Neutral:  0.3,
			Porn:     0.4,
			Sexy:     0.5,
		},
		Photo: &engine.ClassifierOutput{
			Model:  "photo-model",
			Scores: map[string]float32{"high": 0.9, "low": 0.1},
		},
		Anime: &engine.ClassifierOutput{
			Model:  "anime-model",
			Scores: map[string]float32{"r18": 0.8},
		},
		Detections: []engine.Detection{{
			Label: "porn",
			Score: 0.7,
			Box:   engine.Box{X: 1, Y: 2, W: 3, H: 4},
		}},
		Models: []engine.ModelInfo{{
			ID:     "photo-model",
			Kind:   "classifier",
			SHA256: "abc123",
		}},
		ElapsedMs: 42,
	}
	checker := func(context.Context, []byte) (bool, error) { return true, nil }
	client := newTestClient(t, &fakeAnalyzer{analysis: analysis}, checker)

	resp, err := client.Analyze(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	want := &grpcModels.Analysis{
		Verdict:  engine.VerdictExplicit,
		Nsfw:     0.9,
		NsfwText: true,
		Labels: &grpcModels.NSFWLabels{
			Drawings: 0.1,
			Hentai:   0.2,
			Neutral:  0.3,
			Porn:     0.4,
			Sexy:     0.5,
			NsfwText: true,
		},
		Photo: &grpcModels.ScoreMap{
			Model:  "photo-model",
			Scores: map[string]float32{"high": 0.9, "low": 0.1},
		},
		Anime: &grpcModels.ScoreMap{
			Model:  "anime-model",
			Scores: map[string]float32{"r18": 0.8},
		},
		Detections: []*grpcModels.Detection{{
			Label: "porn",
			Score: 0.7,
			Box:   &grpcModels.Box{X: 1, Y: 2, W: 3, H: 4},
		}},
		Models: []*grpcModels.ModelInfo{{
			Id:     "photo-model",
			Kind:   "classifier",
			Sha256: "abc123",
		}},
		ElapsedMs: 42,
	}
	if !proto.Equal(resp, want) {
		t.Fatalf("Analyze() = %v, want %v", resp, want)
	}
}

func TestAnalyzeMinimal(t *testing.T) {
	payload := encodePayload(t)
	client := newTestClient(t, &fakeAnalyzer{}, nil)

	resp, err := client.Analyze(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if resp.GetPhoto() != nil {
		t.Fatalf("Photo = %v, want nil", resp.GetPhoto())
	}
	if resp.GetAnime() != nil {
		t.Fatalf("Anime = %v, want nil", resp.GetAnime())
	}
	if len(resp.GetDetections()) != 0 {
		t.Fatalf("Detections = %v, want empty", resp.GetDetections())
	}
	if len(resp.GetModels()) != 0 {
		t.Fatalf("Models = %v, want empty", resp.GetModels())
	}
	if resp.GetNsfwText() {
		t.Fatalf("NsfwText = true, want false")
	}
	if resp.GetLabels().GetNsfwText() {
		t.Fatalf("Labels.NsfwText = true, want false")
	}
}

func TestAnalyzeErrors(t *testing.T) {
	payload := encodePayload(t)

	_, err := newTestClient(t, &fakeAnalyzer{err: fmt.Errorf("engine: %w", engine.ErrImageTooLarge)}, nil).Analyze(
		context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.ResourceExhausted)

	_, err = newTestClient(t, &fakeAnalyzer{err: errors.New("boom")}, nil).Analyze(
		context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.Internal)

	_, err = newTestClient(t, &fakeAnalyzer{}, nil).Analyze(
		context.Background(), &grpcModels.NSFWRequest{Base64: "not base64!!"})
	assertCode(t, err, codes.InvalidArgument)
}

func TestStartGrpcServerGracefulShutdown(t *testing.T) {
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartGrpcServer(ctx, config.Config{Port: port}, &fakeAnalyzer{}, nil, testLogger())
	}()

	conn := waitForDial(t, fmt.Sprintf("127.0.0.1:%d", port))
	_ = conn.Close()
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StartGrpcServer() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartGrpcServer did not return after context cancellation")
	}
}

func TestStartGrpcServerListenError(t *testing.T) {
	err := StartGrpcServer(context.Background(), config.Config{Port: 70000}, &fakeAnalyzer{}, nil, testLogger())
	if err == nil {
		t.Fatal("StartGrpcServer() error = nil, want listen error")
	}
}

func TestStartGrpcServerGracefulDrain(t *testing.T) {
	port := freePort(t)
	payload := encodePayload(t)
	started := make(chan struct{})
	release := make(chan struct{})
	analyzer := &fakeAnalyzer{fn: func(context.Context, []byte) (engine.Analysis, error) {
		close(started)
		<-release
		return engine.Analysis{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartGrpcServer(ctx, config.Config{Port: port, MaxImageBytes: 1 << 20}, analyzer, nil, testLogger())
	}()
	client := dialPort(t, port)

	rpcErr := make(chan error, 1)
	go func() {
		_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		rpcErr <- err
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("analyzer did not start")
	}

	cancel()
	select {
	case err := <-errCh:
		t.Fatalf("StartGrpcServer() = %v before in-flight RPC drained", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-rpcErr:
		if err != nil {
			t.Fatalf("Detect() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight RPC did not complete")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StartGrpcServer() = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StartGrpcServer did not return after drain")
	}
}

func TestStartGrpcServerForceStop(t *testing.T) {
	override(t, &gracefulTimeout, 50*time.Millisecond)
	port := freePort(t)
	payload := encodePayload(t)
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	analyzer := &fakeAnalyzer{fn: func(context.Context, []byte) (engine.Analysis, error) {
		close(started)
		<-release
		return engine.Analysis{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartGrpcServer(ctx, config.Config{Port: port, MaxImageBytes: 1 << 20}, analyzer, nil, testLogger())
	}()
	client := dialPort(t, port)

	go func() {
		_, _ = client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("analyzer did not start")
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StartGrpcServer() = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartGrpcServer did not return after force stop")
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("server still accepting connections after force stop")
	}
}

func TestMessageLimitFor(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		want  int
	}{
		{"zero", 0, bodyLimitFloor},
		{"negative", -1, bodyLimitFloor},
		{"tiny", 3, bodyLimitFloor},
		{"one MiB", 1 << 20, (1<<20)/3*4 + 4 + bodyLimitOverhead},
		{"huge", math.MaxInt64, math.MaxInt32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageLimitFor(tc.bytes); got != tc.want {
				t.Fatalf("messageLimitFor(%d) = %d, want %d", tc.bytes, got, tc.want)
			}
		})
	}
}

func dialPort(t *testing.T, port int) grpcModels.NSFWClient {
	t.Helper()
	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return grpcModels.NewNSFWClient(conn)
}

func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() { _ = lis.Close() }()
	return lis.Addr().(*net.TCPAddr).Port
}

func waitForDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not accept connections on %s", addr)
	return nil
}
