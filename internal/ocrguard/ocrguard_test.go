package ocrguard

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuardNil(t *testing.T) {
	if Guard(nil, 2, time.Second) != nil {
		t.Fatal("Guard(nil) != nil, want nil")
	}
}

func TestGuardPassesContextAndImage(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	image := []byte("image-bytes")

	var gotCtx context.Context
	var gotImg []byte
	guard := Guard(func(c context.Context, img []byte) (bool, error) {
		gotCtx, gotImg = c, img
		return true, nil
	}, 1, time.Second)

	nsfw, err := guard(ctx, image)
	if err != nil || !nsfw {
		t.Fatalf("guard = %v, %v; want true, nil", nsfw, err)
	}
	if gotCtx.Value(ctxKey{}) != "sentinel" {
		t.Error("guard dropped the caller context value")
	}
	if string(gotImg) != string(image) {
		t.Errorf("guard image = %q, want %q", gotImg, image)
	}
}

func TestGuardAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var called atomic.Bool
	guard := Guard(func(context.Context, []byte) (bool, error) {
		called.Store(true)
		return false, nil
	}, 1, time.Second)

	if _, err := guard(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("guard error = %v, want context.Canceled", err)
	}
	if called.Load() {
		t.Error("checker ran for an already cancelled context")
	}
}

func TestGuardErrorPassThrough(t *testing.T) {
	want := errors.New("ocr broke")
	guard := Guard(func(context.Context, []byte) (bool, error) { return false, want }, 1, time.Second)
	if _, err := guard(context.Background(), nil); !errors.Is(err, want) {
		t.Fatalf("guard error = %v, want %v", err, want)
	}
}

func TestGuardConcurrencyBound(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, peak atomic.Int32
	guard := Guard(func(context.Context, []byte) (bool, error) {
		current := active.Add(1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return true, nil
	}, 2, time.Second)

	done := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := guard(context.Background(), nil)
			done <- err
		}()
	}

	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("two checker calls did not start concurrently")
		}
	}
	select {
	case <-entered:
		t.Fatal("third checker call started before a slot was free")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatalf("guard call error = %v", err)
		}
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrent calls = %d, want at most 2", got)
	}
}

func TestGuardTimeout(t *testing.T) {
	guard := Guard(func(ctx context.Context, _ []byte) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}, 1, 20*time.Millisecond)

	start := time.Now()
	if _, err := guard(context.Background(), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("guard error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("guard blocked %s, want near the 20ms timeout", elapsed)
	}
}

func TestGuardParentCancel(t *testing.T) {
	t.Run("while waiting for a slot", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		guard := Guard(func(context.Context, []byte) (bool, error) {
			entered <- struct{}{}
			<-release
			return true, nil
		}, 1, time.Second)

		go func() { _, _ = guard(context.Background(), nil) }()
		<-entered

		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		defer cancel()
		if _, err := guard(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("guard error = %v, want context.Canceled while the slot is held", err)
		}
	})

	t.Run("while the checker runs", func(t *testing.T) {
		started := make(chan struct{}, 1)
		canceled := make(chan struct{}, 1)
		guard := Guard(func(ctx context.Context, _ []byte) (bool, error) {
			started <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return false, ctx.Err()
		}, 1, time.Minute)

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, err := guard(ctx, nil)
			errCh <- err
		}()

		<-started
		cancel()
		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("guard error = %v, want context.Canceled", err)
		}
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("checker did not observe parent cancellation")
		}
	})
}

func TestGuardPanic(t *testing.T) {
	guard := Guard(func(context.Context, []byte) (bool, error) {
		panic("tesseract exploded")
	}, 1, time.Second)

	_, err := guard(context.Background(), nil)
	if err == nil {
		t.Fatal("guard error = nil, want panic converted to error")
	}
	if !strings.Contains(err.Error(), "tesseract exploded") {
		t.Errorf("guard error = %v, want it to mention the panic", err)
	}
}

func TestGuardSlotReleasedAfterPanic(t *testing.T) {
	var calls atomic.Int32
	guard := Guard(func(context.Context, []byte) (bool, error) {
		if calls.Add(1) == 1 {
			panic("checker exploded")
		}
		return true, nil
	}, 1, time.Second)

	if _, err := guard(context.Background(), nil); err == nil {
		t.Fatal("first guard error = nil, want panic error")
	}
	nsfw, err := guard(context.Background(), nil)
	if err != nil {
		t.Fatalf("second guard error = %v, want nil", err)
	}
	if !nsfw {
		t.Fatal("second guard nsfw = false, want true")
	}
}

func TestGuardSlotReleasedAfterTimeout(t *testing.T) {
	firstDone := make(chan struct{})
	var calls atomic.Int32
	guard := Guard(func(ctx context.Context, _ []byte) (bool, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			close(firstDone)
			return false, ctx.Err()
		}
		return true, nil
	}, 1, 20*time.Millisecond)

	if _, err := guard(context.Background(), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first guard error = %v, want context.DeadlineExceeded", err)
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first checker did not return after the timeout")
	}

	start := time.Now()
	nsfw, err := guard(context.Background(), nil)
	if err != nil {
		t.Fatalf("second guard error = %v, want nil once the slot is free", err)
	}
	if !nsfw {
		t.Fatal("second guard nsfw = false, want true")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("second guard blocked %s, want the slot released after the timeout", elapsed)
	}
}
