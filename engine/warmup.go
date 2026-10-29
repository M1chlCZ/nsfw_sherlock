package engine

import (
	"context"
	"encoding/base64"
	"fmt"
)

const warmUpPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

var warmUpImage = func() []byte {
	data, err := base64.StdEncoding.DecodeString(warmUpPNG)
	if err != nil {
		panic("engine: invalid warm-up image: " + err.Error())
	}
	return data
}()

// WarmUp runs one analysis through the engine.
//
// The call starts every model session and fails on a broken model before the
// first request arrives.
func (e *Engine) WarmUp(ctx context.Context) error {
	if _, err := e.Analyze(ctx, warmUpImage); err != nil {
		return fmt.Errorf("engine: warm-up: %w", err)
	}
	return nil
}
