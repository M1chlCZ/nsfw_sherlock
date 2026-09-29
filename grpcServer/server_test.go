package grpcserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"nsfw_sherlock/engine"
	"nsfw_sherlock/grpcModels"

	"google.golang.org/grpc/codes"
)

func TestRPCTimeout(t *testing.T) {
	override(t, &rpcTimeout, 20*time.Millisecond)
	analyzer := &fakeAnalyzer{fn: func(context.Context, []byte) (engine.Analysis, error) {
		time.Sleep(200 * time.Millisecond)
		return engine.Analysis{}, nil
	}}
	client := newTestClient(t, analyzer, nil)

	start := time.Now()
	_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: encodePayload(t)})
	assertCode(t, err, codes.DeadlineExceeded)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("RPC took %s, want near the 20ms timeout", elapsed)
	}
}

func TestRPCConcurrencyLimiter(t *testing.T) {
	override(t, &rpcConcurrency, 1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()

	analyzer := &fakeAnalyzer{fn: func(context.Context, []byte) (engine.Analysis, error) {
		entered <- struct{}{}
		<-release
		return engine.Analysis{}, nil
	}}
	client := newTestClient(t, analyzer, nil)
	payload := encodePayload(t)

	firstErr := make(chan error, 1)
	go func() {
		_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
		firstErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first RPC did not reach the analyzer")
	}

	_, err := client.Detect(context.Background(), &grpcModels.NSFWRequest{Base64: payload})
	assertCode(t, err, codes.ResourceExhausted)

	unblock()
	if err := <-firstErr; err != nil {
		t.Fatalf("first RPC error = %v, want nil", err)
	}
}
