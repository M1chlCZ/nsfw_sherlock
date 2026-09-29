package engine

import (
	"math"
	"testing"
)

func TestSoftmax(t *testing.T) {
	out, err := softmax([]float32{1, 2, 3})
	if err != nil {
		t.Fatalf("softmax: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(softmax) = %d, want 3", len(out))
	}
	var sum float64
	for i, v := range out {
		if v <= 0 || v >= 1 {
			t.Errorf("out[%d] = %v, want strictly between 0 and 1", i, v)
		}
		if i > 0 && out[i] <= out[i-1] {
			t.Errorf("out[%d] = %v, want greater than out[%d] = %v", i, out[i], i-1, out[i-1])
		}
		sum += float64(v)
	}
	if math.Abs(sum-1) > 1e-5 {
		t.Errorf("softmax sum = %v, want 1 (±1e-5)", sum)
	}

	large, err := softmax([]float32{1000, 1001})
	if err != nil {
		t.Fatalf("softmax(large): %v", err)
	}
	for i, v := range large {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("large softmax[%d] = %v, want finite", i, v)
		}
	}
	if sum := float64(large[0]) + float64(large[1]); math.Abs(sum-1) > 1e-5 {
		t.Errorf("large softmax sum = %v, want 1 (±1e-5)", sum)
	}

	for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, err := softmax([]float32{1, bad}); err == nil {
			t.Errorf("softmax(%v) = nil error, want error", bad)
		}
	}
}

func TestApplyActivation(t *testing.T) {
	t.Run("softmax", func(t *testing.T) {
		out, err := applyActivation([]float32{1, 2, 3}, ActivationSoftmax)
		if err != nil {
			t.Fatalf("applyActivation(softmax): %v", err)
		}
		var sum float64
		for _, v := range out {
			sum += float64(v)
		}
		if math.Abs(sum-1) > 1e-5 {
			t.Errorf("softmax activation sum = %v, want 1 (±1e-5)", sum)
		}
	})

	t.Run("none returns input", func(t *testing.T) {
		in := []float32{0.25, 0.5, 0.75}
		out, err := applyActivation(in, ActivationNone)
		if err != nil {
			t.Fatalf("applyActivation(none): %v", err)
		}
		if len(out) != len(in) {
			t.Fatalf("len(out) = %d, want %d", len(out), len(in))
		}
		for i := range in {
			if out[i] != in[i] {
				t.Errorf("out[%d] = %v, want %v unchanged", i, out[i], in[i])
			}
		}
	})

	t.Run("unknown activation", func(t *testing.T) {
		if _, err := applyActivation([]float32{1}, "sigmoid"); err == nil {
			t.Fatal("applyActivation(sigmoid) = nil error, want error")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if _, err := applyActivation(nil, ActivationSoftmax); err == nil {
			t.Fatal("applyActivation(empty, softmax) = nil error, want error")
		}
		if _, err := applyActivation(nil, ActivationNone); err == nil {
			t.Fatal("applyActivation(empty, none) = nil error, want error")
		}
	})

	t.Run("non-finite input", func(t *testing.T) {
		for _, act := range []string{ActivationSoftmax, ActivationNone} {
			for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
				if _, err := applyActivation([]float32{1, bad}, act); err == nil {
					t.Errorf("applyActivation(%v, %s) = nil error, want error", bad, act)
				}
			}
		}
	})
}

func TestLabelsFromScores(t *testing.T) {
	labels := []string{"drawings", "hentai", "neutral"}
	scores := []float32{0.1, 0.2, 0.7}

	got, err := labelsFromScores(labels, scores)
	if err != nil {
		t.Fatalf("labelsFromScores: %v", err)
	}
	if len(got) != len(labels) {
		t.Fatalf("len(labelsFromScores) = %d, want %d", len(got), len(labels))
	}
	for i, label := range labels {
		if got[label] != scores[i] {
			t.Errorf("got[%q] = %v, want %v", label, got[label], scores[i])
		}
	}

	if _, err := labelsFromScores([]string{"a", "b"}, []float32{1, 2, 3}); err == nil {
		t.Fatal("labelsFromScores(length mismatch) = nil error, want error")
	}
	if _, err := labelsFromScores([]string{"a", "a", "b"}, []float32{1, 2, 3}); err == nil {
		t.Fatal("labelsFromScores(duplicate label) = nil error, want error")
	}
}

func TestInputSpecFromModel(t *testing.T) {
	m := Model{
		Input: Input{
			Width:         448,
			Height:        448,
			Mean:          [3]float32{0.485, 0.456, 0.406},
			Std:           [3]float32{0.229, 0.224, 0.225},
			Normalize:     true,
			Resize:        ResizeStretch,
			Interpolation: InterpolationBilinear,
		},
	}
	want := InputSpec{
		Width:         448,
		Height:        448,
		Mean:          [3]float32{0.485, 0.456, 0.406},
		Std:           [3]float32{0.229, 0.224, 0.225},
		Normalize:     true,
		Resize:        ResizeStretch,
		Interpolation: InterpolationBilinear,
	}
	if got := inputSpecFromModel(m); got != want {
		t.Errorf("inputSpecFromModel = %+v, want %+v", got, want)
	}
}
