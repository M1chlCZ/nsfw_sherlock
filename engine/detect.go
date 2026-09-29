package engine

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

// Box is a detection box in source-image pixel coordinates.
type Box struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	W float32 `json:"w"`
	H float32 `json:"h"`
}

// Detection is one object found by a detector model.
type Detection struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
	Box   Box     `json:"box"`
}

// DetectorGeometry describes how a source image is fitted onto a detector input tensor.
type DetectorGeometry struct {
	Scale  float32
	InputW int
	InputH int
}

type detector struct {
	id     string
	spec   DetectorSpec
	inputW int
	inputH int
	pool   *modelPool
}

func newDetector(m Model, pool *modelPool) (*detector, error) {
	if m.Detector == nil {
		return nil, fmt.Errorf("engine: detector %q: missing detector spec", m.ID)
	}
	if len(m.Detector.Classes) == 0 {
		return nil, fmt.Errorf("engine: detector %q: no classes", m.ID)
	}
	if m.Input.Width <= 0 || m.Input.Height <= 0 {
		return nil, fmt.Errorf("engine: detector %q: invalid input size %dx%d", m.ID, m.Input.Width, m.Input.Height)
	}
	if m.Input.Normalize {
		return nil, fmt.Errorf("engine: detector %q: input normalize must be false", m.ID)
	}
	if m.Input.Mean != [3]float32{} {
		return nil, fmt.Errorf("engine: detector %q: input mean must be zero, got %v", m.ID, m.Input.Mean)
	}
	if m.Input.Std != [3]float32{} {
		return nil, fmt.Errorf("engine: detector %q: input std must be zero, got %v", m.ID, m.Input.Std)
	}
	if m.Input.Resize != "" && m.Input.Resize != ResizeStretch {
		return nil, fmt.Errorf("engine: detector %q: unsupported resize mode %q", m.ID, m.Input.Resize)
	}
	if m.Input.CropPct != 0 {
		return nil, fmt.Errorf("engine: detector %q: input crop_pct must be 0, got %v", m.ID, m.Input.CropPct)
	}
	if m.Input.Interpolation != "" && m.Input.Interpolation != InterpolationBilinear {
		return nil, fmt.Errorf(
			"engine: detector %q: input interpolation must be empty or %q, got %q",
			m.ID,
			InterpolationBilinear,
			m.Input.Interpolation,
		)
	}
	if pool == nil {
		return nil, fmt.Errorf("engine: detector %q: nil model pool", m.ID)
	}
	return &detector{
		id:     m.ID,
		spec:   *m.Detector,
		inputW: m.Input.Width,
		inputH: m.Input.Height,
		pool:   pool,
	}, nil
}

func (d *detector) run(ctx context.Context, img image.Image) ([]Detection, error) {
	if img == nil {
		return nil, errors.New("engine: detector: nil image")
	}
	data, lb := preprocessDetector(img, d.inputW, d.inputH)
	shape := ort.NewShape(1, 3, int64(d.inputH), int64(d.inputW))
	outputs, err := d.pool.run(ctx, data, shape)
	if err != nil {
		return nil, fmt.Errorf("detector %q: %w", d.id, err)
	}
	defer destroyValues(outputs)

	tensor, err := float32Output(outputs)
	if err != nil {
		return nil, fmt.Errorf("detector %q: %w", d.id, err)
	}
	b := img.Bounds()
	dets, err := decodeYOLO(tensor.GetData(), tensor.GetShape(), d.spec, lb, b.Dx(), b.Dy())
	if err != nil {
		return nil, fmt.Errorf("detector %q: %w", d.id, err)
	}
	return dets, nil
}

func computeDetectorGeometry(srcW, srcH, dstW, dstH int) DetectorGeometry {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return DetectorGeometry{InputW: dstW, InputH: dstH}
	}
	scale := math.Min(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	return DetectorGeometry{
		Scale:  float32(scale),
		InputW: dstW,
		InputH: dstH,
	}
}

func roundHalfEven(f float64) int {
	return int(math.RoundToEven(f))
}

func resizeLinearNoAA(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	return resizeLinearNoAAFrame(src, src.Bounds().Dx(), src.Bounds().Dy(), dstW, dstH)
}

func resizeLinearNoAAPadded(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	side := max(src.Bounds().Dx(), src.Bounds().Dy())
	return resizeLinearNoAAFrame(src, side, side, dstW, dstH)
}

func resizeLinearNoAAFrame(src *image.NRGBA, frameW, frameH, dstW, dstH int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	contentW, contentH := src.Bounds().Dx(), src.Bounds().Dy()
	if frameW <= 0 || frameH <= 0 || dstW <= 0 || dstH <= 0 || contentW <= 0 || contentH <= 0 {
		return dst
	}
	scaleX := float64(frameW) / float64(dstW)
	scaleY := float64(frameH) / float64(dstH)
	sample := func(x, y int) (r, g, b float64) {
		if x < 0 || y < 0 || x >= contentW || y >= contentH {
			return 0, 0, 0
		}
		i := y*src.Stride + x*4
		return float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2])
	}
	for y := range dstH {
		y0, wy := linearTap((float64(y)+0.5)*scaleY-0.5, frameH)
		y1 := min(y0+1, frameH-1)
		row := y * dst.Stride
		for x := range dstW {
			x0, wx := linearTap((float64(x)+0.5)*scaleX-0.5, frameW)
			x1 := min(x0+1, frameW-1)
			w00 := (1 - wx) * (1 - wy)
			w10 := wx * (1 - wy)
			w01 := (1 - wx) * wy
			w11 := wx * wy
			r00, g00, b00 := sample(x0, y0)
			r10, g10, b10 := sample(x1, y0)
			r01, g01, b01 := sample(x0, y1)
			r11, g11, b11 := sample(x1, y1)
			i := row + x*4
			dst.Pix[i] = linearPixel(w00*r00 + w10*r10 + w01*r01 + w11*r11)
			dst.Pix[i+1] = linearPixel(w00*g00 + w10*g10 + w01*g01 + w11*g11)
			dst.Pix[i+2] = linearPixel(w00*b00 + w10*b10 + w01*b01 + w11*b11)
			dst.Pix[i+3] = 0xff
		}
	}
	return dst
}

func linearTap(fx float64, size int) (index int, weight float64) {
	if fx <= 0 {
		return 0, 0
	}
	last := float64(size - 1)
	if fx >= last {
		return size - 1, 0
	}
	i := int(math.Floor(fx))
	return i, fx - float64(i)
}

func linearPixel(v float64) uint8 {
	return uint8(math.Round(min(max(v, 0), 255)))
}

func preprocessDetector(img image.Image, dstW, dstH int) ([]float32, DetectorGeometry) {
	if img == nil || dstW <= 0 || dstH <= 0 {
		return nil, DetectorGeometry{InputW: dstW, InputH: dstH}
	}
	b := img.Bounds()
	geom := computeDetectorGeometry(b.Dx(), b.Dy(), dstW, dstH)

	out := make([]float32, 3*dstW*dstH)
	rendered := renderDetectorInput(img, geom)
	plane := dstW * dstH
	for y := range dstH {
		for x := range dstW {
			i := rendered.PixOffset(x, y)
			j := y*dstW + x
			out[j] = float32(rendered.Pix[i+2]) / 255
			out[plane+j] = float32(rendered.Pix[i+1]) / 255
			out[2*plane+j] = float32(rendered.Pix[i+0]) / 255
		}
	}
	return out, geom
}

func renderDetectorInput(img image.Image, geom DetectorGeometry) *image.NRGBA {
	src := img
	if o, ok := img.(interface{ Opaque() bool }); !ok || !o.Opaque() {
		src = flattenOpaque(img)
	}
	nrgba, ok := src.(*image.NRGBA)
	if !ok {
		b := src.Bounds()
		nrgba = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(nrgba, nrgba.Bounds(), src, b.Min, draw.Src)
	}
	return resizeLinearNoAAPadded(nrgba, geom.InputW, geom.InputH)
}

func decodeYOLO(
	out []float32,
	shape []int64,
	spec DetectorSpec,
	geom DetectorGeometry,
	srcW, srcH int,
) ([]Detection, error) {
	rows := 4 + len(spec.Classes)
	if len(spec.Classes) == 0 {
		return nil, errors.New("decode: no classes")
	}
	if len(shape) != 3 || shape[0] != 1 {
		return nil, fmt.Errorf("decode: shape %v, want [1, %d, N] or [1, N, %d]", shape, rows, rows)
	}
	if geom.Scale <= 0 {
		return nil, fmt.Errorf("decode: invalid detector scale %v", geom.Scale)
	}
	var n, stride int
	channelMajor := false
	switch {
	case int(shape[1]) == rows:
		channelMajor = true
		n = int(shape[2])
		stride = n
	case int(shape[2]) == rows:
		n = int(shape[1])
		stride = rows
	default:
		return nil, fmt.Errorf("decode: shape %v does not match %d rows for %d classes", shape, rows, len(spec.Classes))
	}
	if n <= 0 {
		return nil, fmt.Errorf("decode: candidate count %d must be positive", n)
	}
	if len(out)/rows < n {
		return nil, fmt.Errorf("decode: output has %d values, want %d candidates of %d rows", len(out), n, rows)
	}
	at := func(row, i int) float32 {
		if channelMajor {
			return out[row*stride+i]
		}
		return out[i*stride+row]
	}

	dets := []Detection{}
	for i := range n {
		best, bestScore := 0, at(4, i)
		for c := 1; c < len(spec.Classes); c++ {
			if s := at(4+c, i); s > bestScore {
				best, bestScore = c, s
			}
		}
		if !isFinite(bestScore) || bestScore <= spec.ScoreThreshold {
			continue
		}
		cx, cy := at(0, i), at(1, i)
		w, h := at(2, i), at(3, i)
		if !isFinite(cx) || !isFinite(cy) || !isFinite(w) || !isFinite(h) {
			continue
		}
		w, h = w/geom.Scale, h/geom.Scale
		if w <= 0 || h <= 0 {
			continue
		}
		x := cx/geom.Scale - w/2
		y := cy/geom.Scale - h/2
		x2 := min(x+w, float32(srcW))
		y2 := min(y+h, float32(srcH))
		x1 := clampFloat(x, float32(srcW))
		y1 := clampFloat(y, float32(srcH))
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		dets = append(dets, Detection{
			Label: spec.Classes[best],
			Score: bestScore,
			Box:   Box{X: x1, Y: y1, W: x2 - x1, H: y2 - y1},
		})
	}
	return nms(dets, spec.IoUThreshold), nil
}

func nms(dets []Detection, iouThreshold float32) []Detection {
	ordered := slices.Clone(dets)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Score > ordered[j].Score })
	kept := make([]Detection, 0, len(ordered))
	suppressed := make([]bool, len(ordered))
	for i := range ordered {
		if suppressed[i] {
			continue
		}
		kept = append(kept, ordered[i])
		for j := i + 1; j < len(ordered); j++ {
			if suppressed[j] {
				continue
			}
			if boxIoU(ordered[i].Box, ordered[j].Box) > iouThreshold {
				suppressed[j] = true
			}
		}
	}
	return kept
}

func boxIoU(a, b Box) float32 {
	ax1, ay1 := a.X, a.Y
	ax2, ay2 := a.X+a.W, a.Y+a.H
	bx1, by1 := b.X, b.Y
	bx2, by2 := b.X+b.W, b.Y+b.H
	interW := min(ax2, bx2) - max(ax1, bx1)
	interH := min(ay2, by2) - max(ay1, by1)
	if interW <= 0 || interH <= 0 {
		return 0
	}
	inter := interW * interH
	union := a.W*a.H + b.W*b.H - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func clampFloat(v, hi float32) float32 {
	return min(max(v, 0), hi)
}
