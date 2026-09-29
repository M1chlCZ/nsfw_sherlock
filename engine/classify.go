package engine

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	ort "github.com/yalue/onnxruntime_go"
)

type classifier struct {
	id         string
	labels     []string
	activation string
	spec       InputSpec
	pool       *modelPool
}

func newClassifier(m Model, pool *modelPool) (*classifier, error) {
	if len(m.Labels) == 0 {
		return nil, fmt.Errorf("engine: classifier %q: no labels", m.ID)
	}
	if m.Output.Activation != ActivationSoftmax && m.Output.Activation != ActivationNone {
		return nil, fmt.Errorf("engine: classifier %q: invalid activation %q", m.ID, m.Output.Activation)
	}
	if m.Input.Width <= 0 || m.Input.Height <= 0 {
		return nil, fmt.Errorf("engine: classifier %q: invalid input size %dx%d", m.ID, m.Input.Width, m.Input.Height)
	}
	if pool == nil {
		return nil, fmt.Errorf("engine: classifier %q: nil model pool", m.ID)
	}
	return &classifier{
		id:         m.ID,
		labels:     m.Labels,
		activation: m.Output.Activation,
		spec:       inputSpecFromModel(m),
		pool:       pool,
	}, nil
}

func (c *classifier) run(ctx context.Context, img image.Image) (map[string]float32, error) {
	data, err := Preprocess(img, c.spec)
	if err != nil {
		return nil, fmt.Errorf("classifier %q: preprocess: %w", c.id, err)
	}
	shape := ort.NewShape(1, 3, int64(c.spec.Height), int64(c.spec.Width))
	outputs, err := c.pool.run(ctx, data, shape)
	if err != nil {
		return nil, fmt.Errorf("classifier %q: %w", c.id, err)
	}
	defer destroyValues(outputs)

	tensor, err := float32Output(outputs)
	if err != nil {
		return nil, fmt.Errorf("classifier %q: %w", c.id, err)
	}
	activated, err := applyActivation(tensor.GetData(), c.activation)
	if err != nil {
		return nil, fmt.Errorf("classifier %q: %w", c.id, err)
	}
	labels, err := labelsFromScores(c.labels, activated)
	if err != nil {
		return nil, fmt.Errorf("classifier %q: %w", c.id, err)
	}
	return labels, nil
}

func float32Output(outputs []ort.Value) (*ort.Tensor[float32], error) {
	if len(outputs) == 0 || outputs[0] == nil {
		return nil, errors.New("model returned no outputs")
	}
	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("model output type %T, want *ort.Tensor[float32]", outputs[0])
	}
	return tensor, nil
}

func inputSpecFromModel(m Model) InputSpec {
	return InputSpec{
		Width:         m.Input.Width,
		Height:        m.Input.Height,
		Mean:          m.Input.Mean,
		Std:           m.Input.Std,
		Normalize:     m.Input.Normalize,
		Resize:        m.Input.Resize,
		CropPct:       m.Input.CropPct,
		Interpolation: m.Input.Interpolation,
	}
}

func applyActivation(scores []float32, activation string) ([]float32, error) {
	if len(scores) == 0 {
		return nil, errors.New("activation: empty scores")
	}
	if err := validateScores(scores); err != nil {
		return nil, fmt.Errorf("activation: %w", err)
	}
	switch activation {
	case ActivationSoftmax:
		return softmax(scores)
	case ActivationNone:
		return scores, nil
	default:
		return nil, fmt.Errorf("activation: unknown activation %q", activation)
	}
}

func softmax(scores []float32) ([]float32, error) {
	if len(scores) == 0 {
		return nil, errors.New("softmax: empty scores")
	}
	if err := validateScores(scores); err != nil {
		return nil, fmt.Errorf("softmax: %w", err)
	}
	max := float64(scores[0])
	for _, s := range scores[1:] {
		if float64(s) > max {
			max = float64(s)
		}
	}
	out := make([]float32, len(scores))
	sum := 0.0
	for i, s := range scores {
		p := math.Exp(float64(s) - max)
		out[i] = float32(p)
		sum += p
	}
	for i := range out {
		out[i] = float32(float64(out[i]) / sum)
	}
	return out, nil
}

func validateScores(scores []float32) error {
	for i, s := range scores {
		if !isFinite(s) {
			return fmt.Errorf("score %d is not finite: %v", i, s)
		}
	}
	return nil
}

func labelsFromScores(labels []string, scores []float32) (map[string]float32, error) {
	if len(labels) != len(scores) {
		return nil, fmt.Errorf("engine: %d labels for %d scores", len(labels), len(scores))
	}
	out := make(map[string]float32, len(labels))
	for i, label := range labels {
		if _, ok := out[label]; ok {
			return nil, fmt.Errorf("engine: duplicate label %q", label)
		}
		out[label] = scores[i]
	}
	return out, nil
}
