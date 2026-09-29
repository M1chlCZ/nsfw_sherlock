package ocrguard

import (
	"context"
	"fmt"
	"time"
)

// Guard bounds f to concurrency in-flight calls and a per-call timeout.
func Guard(f func(ctx context.Context, img []byte) (bool, error), concurrency int, timeout time.Duration) func(ctx context.Context, img []byte) (bool, error) {
	if f == nil {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	slots := make(chan struct{}, concurrency)
	return func(ctx context.Context, img []byte) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return false, ctx.Err()
		}

		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		type result struct {
			nsfw bool
			err  error
		}
		done := make(chan result, 1)
		go func() {
			defer func() { <-slots }()
			defer func() {
				if r := recover(); r != nil {
					done <- result{err: fmt.Errorf("ocrguard: panic: %v", r)}
				}
			}()
			nsfw, err := f(callCtx, img)
			done <- result{nsfw: nsfw, err: err}
		}()

		select {
		case res := <-done:
			return res.nsfw, res.err
		case <-callCtx.Done():
			return false, callCtx.Err()
		}
	}
}
