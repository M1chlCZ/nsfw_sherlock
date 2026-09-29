package engine

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"testing"
)

func requireNear(t *testing.T, name string, got, want, tol float32) {
	t.Helper()
	if diff := got - want; diff < -tol || diff > tol {
		t.Errorf("%s = %v, want %v (±%v)", name, got, want, tol)
	}
}

func classNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("class%d", i)
	}
	return names
}

func fakeYOLOOutput(classes, n int, channelMajor bool, at func(candidate, row int) float32) []float32 {
	rows := 4 + classes
	out := make([]float32, rows*n)
	for i := 0; i < n; i++ {
		for r := 0; r < rows; r++ {
			idx := r*n + i
			if !channelMajor {
				idx = i*rows + r
			}
			out[idx] = at(i, r)
		}
	}
	return out
}

func requireDetections(t *testing.T, got, want []Detection) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(detections) = %d, want %d (got: %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Label != want[i].Label {
			t.Errorf("detections[%d].Label = %q, want %q", i, got[i].Label, want[i].Label)
		}
		requireNear(t, fmt.Sprintf("detections[%d].Score", i), got[i].Score, want[i].Score, 1e-4)
		requireNear(t, fmt.Sprintf("detections[%d].Box.X", i), got[i].Box.X, want[i].Box.X, 1e-4)
		requireNear(t, fmt.Sprintf("detections[%d].Box.Y", i), got[i].Box.Y, want[i].Box.Y, 1e-4)
		requireNear(t, fmt.Sprintf("detections[%d].Box.W", i), got[i].Box.W, want[i].Box.W, 1e-4)
		requireNear(t, fmt.Sprintf("detections[%d].Box.H", i), got[i].Box.H, want[i].Box.H, 1e-4)
	}
}

func TestComputeDetectorGeometry(t *testing.T) {
	cases := []struct {
		name                   string
		srcW, srcH, dstW, dstH int
		scale                  float32
	}{
		{
			name: "landscape 640x480 into 320x320",
			srcW: 640, srcH: 480, dstW: 320, dstH: 320,
			scale: 0.5,
		},
		{
			name: "portrait 480x640 into 320x320",
			srcW: 480, srcH: 640, dstW: 320, dstH: 320,
			scale: 0.5,
		},
		{
			name: "square 640x640 into 320x320",
			srcW: 640, srcH: 640, dstW: 320, dstH: 320,
			scale: 0.5,
		},
		{
			name: "non-integer scale 321x200 into 320x320",
			srcW: 321, srcH: 200, dstW: 320, dstH: 320,
			scale: float32(320.0 / 321.0),
		},
		{
			name: "upscale 100x50 into 640x640",
			srcW: 100, srcH: 50, dstW: 640, dstH: 640,
			scale: 6.4,
		},
		{
			name: "degenerate source keeps the input size",
			srcW: 0, srcH: 10, dstW: 320, dstH: 320,
			scale: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			geom := computeDetectorGeometry(tc.srcW, tc.srcH, tc.dstW, tc.dstH)
			requireNear(t, "Scale", geom.Scale, tc.scale, 1e-5)
			if geom.InputW != tc.dstW || geom.InputH != tc.dstH {
				t.Errorf("input size = %dx%d, want %dx%d", geom.InputW, geom.InputH, tc.dstW, tc.dstH)
			}
		})
	}
}

func grayNRGBA(w, h int, values ...uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if len(values) != w*h {
		panic("grayNRGBA: value count does not match size")
	}
	for i, v := range values {
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = v, v, v, 0xff
	}
	return img
}

func TestResizeLinearNoAA(t *testing.T) {
	cases := []struct {
		name       string
		srcW, srcH int
		values     []uint8
		dstW, dstH int
		want       []uint8
	}{
		{
			name: "upscale 1x2 to 1x4",
			srcW: 2, srcH: 1, values: []uint8{0, 255},
			dstW: 4, dstH: 1, want: []uint8{0, 64, 191, 255},
		},
		{
			name: "downscale 1x4 to 1x2 samples without antialiasing",
			srcW: 4, srcH: 1, values: []uint8{0, 85, 170, 255},
			dstW: 2, dstH: 1, want: []uint8{43, 213},
		},
		{
			name: "2x2 checker to 4x4",
			srcW: 2, srcH: 2, values: []uint8{0, 255, 255, 0},
			dstW: 4, dstH: 4,
			want: []uint8{
				0, 64, 191, 255,
				64, 96, 159, 191,
				191, 159, 96, 64,
				255, 191, 64, 0,
			},
		},
		{
			name: "2x3 gradient to 5x3",
			srcW: 3, srcH: 2, values: []uint8{0, 10, 20, 30, 40, 50},
			dstW: 5, dstH: 3,
			want: []uint8{
				0, 4, 10, 16, 20,
				15, 19, 25, 31, 35,
				30, 34, 40, 46, 50,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := grayNRGBA(tc.srcW, tc.srcH, tc.values...)
			got := resizeLinearNoAA(src, tc.dstW, tc.dstH)
			if got.Bounds().Dx() != tc.dstW || got.Bounds().Dy() != tc.dstH {
				t.Fatalf("size = %v, want %dx%d", got.Bounds(), tc.dstW, tc.dstH)
			}
			for y := 0; y < tc.dstH; y++ {
				for x := 0; x < tc.dstW; x++ {
					i := got.PixOffset(x, y)
					want := tc.want[y*tc.dstW+x]
					if got.Pix[i] != want || got.Pix[i+1] != want || got.Pix[i+2] != want {
						t.Errorf("pixel (%d,%d) = (%d,%d,%d), want %d", x, y, got.Pix[i], got.Pix[i+1], got.Pix[i+2], want)
					}
				}
			}
		})
	}
}

func TestResizeLinearNoAAEdgeSampling(t *testing.T) {
	src := grayNRGBA(4, 1, 0, 85, 170, 255)
	got := resizeLinearNoAA(src, 2, 2)
	for y := 0; y < 2; y++ {
		i := got.PixOffset(0, y)
		j := got.PixOffset(1, y)
		if got.Pix[i] != 43 || got.Pix[j] != 213 {
			t.Errorf("row %d = [%d %d], want [43 213]", y, got.Pix[i], got.Pix[j])
		}
	}
}

func TestDecodeYOLO(t *testing.T) {
	const classes = 18
	box := [4]float32{160, 160, 20, 20}
	score := func(i, r int) float32 {
		switch {
		case i != 0:
			return 0.01
		case r < 4:
			return box[r]
		case r == 4+5:
			return 0.9
		default:
			return 0.05
		}
	}
	spec := DetectorSpec{Classes: classNames(classes), ScoreThreshold: 0.25, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)

	out := fakeYOLOOutput(classes, 3, true, score)
	got, err := decodeYOLO(out, []int64{1, 22, 3}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}

	want := []Detection{{
		Label: "class5",
		Score: 0.9,
		Box:   Box{X: 300, Y: 300, W: 40, H: 40},
	}}
	requireDetections(t, got, want)

	empty, err := decodeYOLO(
		fakeYOLOOutput(classes, 2, true, func(int, int) float32 { return 0.01 }),
		[]int64{1, 22, 2}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO(no detections): %v", err)
	}
	if empty == nil {
		t.Error("decodeYOLO(no detections) = nil, want non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Errorf("decodeYOLO(no detections) = %v, want empty", empty)
	}
}

func TestDecodeYOLOTransposed(t *testing.T) {
	const classes = 18
	box := [4]float32{160, 160, 20, 20}
	score := func(i, r int) float32 {
		switch {
		case i != 0:
			return 0.01
		case r < 4:
			return box[r]
		case r == 4+5:
			return 0.9
		default:
			return 0.05
		}
	}
	spec := DetectorSpec{Classes: classNames(classes), ScoreThreshold: 0.25, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)

	channelMajor, err := decodeYOLO(fakeYOLOOutput(classes, 3, true, score), []int64{1, 22, 3}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO(channel-major): %v", err)
	}
	transposed, err := decodeYOLO(fakeYOLOOutput(classes, 3, false, score), []int64{1, 3, 22}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO(transposed): %v", err)
	}

	want := []Detection{{
		Label: "class5",
		Score: 0.9,
		Box:   Box{X: 300, Y: 300, W: 40, H: 40},
	}}
	requireDetections(t, transposed, want)
	requireDetections(t, transposed, channelMajor)
}

func TestNMS(t *testing.T) {
	t.Run("suppresses overlapping boxes of any label", func(t *testing.T) {
		top := Detection{Label: "labelA", Score: 0.9, Box: Box{X: 100, Y: 100, W: 20, H: 20}}
		suppressedSame := Detection{Label: "labelA", Score: 0.8, Box: Box{X: 102, Y: 100, W: 20, H: 20}}
		suppressedOther := Detection{Label: "labelB", Score: 0.85, Box: Box{X: 100, Y: 100, W: 20, H: 20}}
		far := Detection{Label: "labelB", Score: 0.7, Box: Box{X: 300, Y: 300, W: 20, H: 20}}

		got := nms([]Detection{suppressedSame, far, top, suppressedOther}, 0.45)

		want := []Detection{top, far}
		if len(got) != len(want) {
			t.Fatalf("len(nms) = %d, want %d (got: %v)", len(got), len(want), got)
		}
		for i := range want {
			if got[i].Label != want[i].Label || got[i].Score != want[i].Score {
				t.Errorf("nms[%d] = {%s %v}, want {%s %v}", i, got[i].Label, got[i].Score, want[i].Label, want[i].Score)
			}
		}
	})

	t.Run("keeps boxes under the iou threshold", func(t *testing.T) {
		a := Detection{Label: "labelA", Score: 0.9, Box: Box{X: 100, Y: 100, W: 20, H: 20}}
		b := Detection{Label: "labelB", Score: 0.8, Box: Box{X: 110, Y: 100, W: 20, H: 20}}

		got := nms([]Detection{a, b}, 0.45)
		if len(got) != 2 {
			t.Fatalf("len(nms) = %d, want 2 (got: %v)", len(got), got)
		}
	})
}

func TestDetectorScoreAndDegenerateFilter(t *testing.T) {
	const classes = 3
	type cand struct {
		cx, cy, w, h float32
		score        float32
	}
	cands := []cand{
		{cx: 160, cy: 160, w: 20, h: 20, score: 0.4},
		{cx: 160, cy: 160, w: 0, h: 20, score: 0.9},
		{cx: 160, cy: 160, w: -5, h: 20, score: 0.8},
		{cx: 0, cy: 160, w: 10, h: 10, score: 0.7},
		{cx: 100, cy: 100, w: 10, h: 10, score: 0.5},
		{cx: 260, cy: 100, w: 10, h: 10, score: 0.5 + 1e-6},
	}
	out := fakeYOLOOutput(classes, len(cands), true, func(i, r int) float32 {
		c := cands[i]
		switch {
		case r < 4:
			return [4]float32{c.cx, c.cy, c.w, c.h}[r]
		case r == 4+1:
			return c.score
		default:
			return 0.01
		}
	})
	spec := DetectorSpec{Classes: classNames(classes), ScoreThreshold: 0.5, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)

	got, err := decodeYOLO(out, []int64{1, 4 + classes, int64(len(cands))}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}

	want := []Detection{
		{Label: "class1", Score: 0.7, Box: Box{X: 0, Y: 310, W: 10, H: 20}},
		{Label: "class1", Score: 0.5 + 1e-6, Box: Box{X: 510, Y: 190, W: 20, H: 20}},
	}
	requireDetections(t, got, want)
}

func TestDetectorBoxClippedToSource(t *testing.T) {
	const classes = 3
	type cand struct {
		cx, cy, w, h float32
		score        float32
	}
	cands := []cand{
		{cx: 315, cy: 240, w: 20, h: 20, score: 0.9},
		{cx: -100, cy: 160, w: 10, h: 10, score: 0.8},
		{cx: 160, cy: -200, w: 10, h: 10, score: 0.7},
	}
	out := fakeYOLOOutput(classes, len(cands), true, func(i, r int) float32 {
		c := cands[i]
		switch {
		case r < 4:
			return [4]float32{c.cx, c.cy, c.w, c.h}[r]
		case r == 4:
			return c.score
		default:
			return 0.01
		}
	})
	spec := DetectorSpec{Classes: classNames(classes), ScoreThreshold: 0.5, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)

	got, err := decodeYOLO(out, []int64{1, 4 + classes, int64(len(cands))}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}

	want := []Detection{
		{Label: "class0", Score: 0.9, Box: Box{X: 610, Y: 460, W: 30, H: 20}},
	}
	requireDetections(t, got, want)
}

func TestPreprocessDetector(t *testing.T) {
	t.Run("scales to the top-left and pads with black", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 640, 480))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 100, G: 150, B: 200, A: 255}), image.Point{}, draw.Src)

		out, geom := preprocessDetector(img, 320, 320)

		if len(out) != 3*320*320 {
			t.Fatalf("len(out) = %d, want %d", len(out), 3*320*320)
		}
		requireNear(t, "Scale", geom.Scale, 0.5, 1e-5)
		if geom.InputW != 320 || geom.InputH != 320 {
			t.Fatalf("unexpected geometry %+v", geom)
		}

		plane := 320 * 320
		content := 1*320 + 1
		requireNear(t, "content B", out[content], 200.0/255, 0.02)
		requireNear(t, "content G", out[plane+content], 150.0/255, 0.02)
		requireNear(t, "content R", out[2*plane+content], 100.0/255, 0.02)

		for _, idx := range []int{240 * 320, 300*320 + 300} {
			if out[idx] != 0 || out[plane+idx] != 0 || out[2*plane+idx] != 0 {
				t.Errorf("padding at %d = [%v %v %v], want black", idx, out[idx], out[plane+idx], out[2*plane+idx])
			}
		}
	})

	t.Run("portrait source pads the right side", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 480, 640))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)

		out, geom := preprocessDetector(img, 320, 320)
		requireNear(t, "Scale", geom.Scale, 0.5, 1e-5)

		plane := 320 * 320
		idx := 1*320 + 300
		if out[idx] != 0 || out[plane+idx] != 0 || out[2*plane+idx] != 0 {
			t.Errorf("right padding = [%v %v %v], want black", out[idx], out[plane+idx], out[2*plane+idx])
		}
	})

	t.Run("BGR channel order", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 4, 4))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)

		out, geom := preprocessDetector(img, 4, 4)

		if geom.Scale != 1 || geom.InputW != 4 || geom.InputH != 4 {
			t.Fatalf("unexpected geometry %+v", geom)
		}
		plane := 4 * 4
		requireNear(t, "B", out[0], 0, 1e-4)
		requireNear(t, "G", out[plane], 0, 1e-4)
		requireNear(t, "R", out[2*plane], 1, 1e-4)
	})

	t.Run("non-opaque source is flattened", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 255, B: 0, A: 0})
			}
		}

		out, _ := preprocessDetector(img, 4, 4)

		plane := 4 * 4
		requireNear(t, "stored green of transparent pixel", out[plane], 1, 0.02)
	})

	t.Run("content edge blends with the right padding", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 1, 2))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{G: 255, A: 255}), image.Point{}, draw.Src)

		out, geom := preprocessDetector(img, 3, 3)
		requireNear(t, "Scale", geom.Scale, 1.5, 1e-6)

		plane := 3 * 3
		requireNear(t, "content at x=0", out[plane+0], 1, 0.01)
		requireNear(t, "blend at x=1", out[plane+1], 0.5, 0.01)
		if out[plane+2] != 0 {
			t.Errorf("padding at x=2 = %v, want 0", out[plane+2])
		}
	})
}

func TestDecodeYOLORejectsBadShapes(t *testing.T) {
	spec3 := DetectorSpec{Classes: classNames(3), ScoreThreshold: 0.5, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)
	data := make([]float32, 7)
	cases := []struct {
		name  string
		out   []float32
		shape []int64
		spec  DetectorSpec
		geom  DetectorGeometry
	}{
		{"short data", nil, []int64{1, 7, 1}, spec3, geom},
		{"no shape", data, nil, spec3, geom},
		{"wrong rank", data, []int64{7, 1}, spec3, geom},
		{"batch not one", data, []int64{2, 7, 1}, spec3, geom},
		{"unknown row count", data, []int64{1, 8, 1}, spec3, geom},
		{"zero candidates", data, []int64{1, 7, 0}, spec3, geom},
		{"too little data for candidates", data, []int64{1, 7, 2}, spec3, geom},
		{"huge candidate count", data, []int64{1, 7, math.MaxInt64}, spec3, geom},
		{"no classes", make([]float32, 8), []int64{1, 4, 2}, DetectorSpec{ScoreThreshold: 0.5, IoUThreshold: 0.45}, geom},
		{"invalid geometry", data, []int64{1, 7, 1}, spec3, DetectorGeometry{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeYOLO(tc.out, tc.shape, tc.spec, tc.geom, 640, 480)
			if err == nil {
				t.Fatalf("decodeYOLO() = %v, nil; want error", got)
			}
			if got != nil {
				t.Errorf("decodeYOLO() detections = %v, want nil on error", got)
			}
		})
	}
}

func TestDecodeYOLONonFinite(t *testing.T) {
	const classes = 3
	type cand struct {
		cx, cy, w, h, score float32
	}
	cands := []cand{
		{cx: 160, cy: 160, w: 20, h: 20, score: 0.9},
		{cx: 160, cy: 160, w: 20, h: 20, score: float32(math.NaN())},
		{cx: float32(math.Inf(1)), cy: 160, w: 20, h: 20, score: 0.8},
		{cx: 160, cy: 160, w: float32(math.NaN()), h: 20, score: 0.7},
	}
	out := fakeYOLOOutput(classes, len(cands), true, func(i, r int) float32 {
		c := cands[i]
		switch {
		case r < 4:
			return [4]float32{c.cx, c.cy, c.w, c.h}[r]
		case r == 4:
			return c.score
		default:
			return 0.01
		}
	})
	spec := DetectorSpec{Classes: classNames(classes), ScoreThreshold: 0.5, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(640, 480, 320, 320)

	got, err := decodeYOLO(out, []int64{1, 4 + classes, int64(len(cands))}, spec, geom, 640, 480)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}
	want := []Detection{{Label: "class0", Score: 0.9, Box: Box{X: 300, Y: 300, W: 40, H: 40}}}
	requireDetections(t, got, want)
}

func TestRoundHalfEven(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0.5, 0}, {1.5, 2}, {2.5, 2}, {3.5, 4}, {4.5, 4},
		{2.4, 2}, {2.6, 3}, {-1.5, -2}, {-2.5, -2},
	}
	for _, tc := range cases {
		if got := roundHalfEven(tc.in); got != tc.want {
			t.Errorf("roundHalfEven(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDecodeYOLOEdgeBoxes(t *testing.T) {
	const classes = 1
	spec := DetectorSpec{Classes: []string{"x"}, ScoreThreshold: 0.25, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(10, 10, 5, 5)

	out := fakeYOLOOutput(classes, 2, true, func(i, r int) float32 {
		switch {
		case i == 0 && r < 4:
			return [4]float32{-100, 5, 2, 2}[r]
		case i == 1 && r < 4:
			return [4]float32{4.5, 5, 2, 2}[r]
		case r == 4:
			return 0.9 - float32(i)*0.1
		default:
			return 0.01
		}
	})
	got, err := decodeYOLO(out, []int64{1, 5, 2}, spec, geom, 10, 10)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}
	want := []Detection{{Label: "x", Score: 0.8, Box: Box{X: 7, Y: 8, W: 3, H: 2}}}
	requireDetections(t, got, want)
}

func TestDecodeYOLOInverseScale(t *testing.T) {
	const classes = 1
	spec := DetectorSpec{Classes: []string{"x"}, ScoreThreshold: 0.25, IoUThreshold: 0.45}
	geom := computeDetectorGeometry(10, 10, 5, 5)
	requireNear(t, "Scale", geom.Scale, 0.5, 1e-6)

	out := fakeYOLOOutput(classes, 2, true, func(i, r int) float32 {
		switch {
		case i == 0 && r < 4:
			return [4]float32{2.5, 2.5, 1, 1}[r]
		case i == 1 && r < 4:
			return [4]float32{4.5, 4.5, 2, 2}[r]
		case r == 4:
			return 0.9
		default:
			return 0.01
		}
	})
	got, err := decodeYOLO(out, []int64{1, 5, 2}, spec, geom, 10, 10)
	if err != nil {
		t.Fatalf("decodeYOLO: %v", err)
	}
	want := []Detection{
		{Label: "x", Score: 0.9, Box: Box{X: 4, Y: 4, W: 2, H: 2}},
		{Label: "x", Score: 0.9, Box: Box{X: 7, Y: 7, W: 3, H: 3}},
	}
	requireDetections(t, got, want)
}

func TestNewDetectorInputConfig(t *testing.T) {
	pool, err := newModelPoolWithFactory(func() (modelRunner, error) { return &fakeRunner{}, nil }, 1)
	if err != nil {
		t.Fatalf("newModelPoolWithFactory: %v", err)
	}
	defer pool.close()

	base := Model{
		ID:       "d",
		Input:    Input{Width: 320, Height: 320},
		Detector: &DetectorSpec{Classes: []string{"x"}, ScoreThreshold: 0.3, IoUThreshold: 0.5},
	}
	if _, err := newDetector(base, pool); err != nil {
		t.Fatalf("newDetector(valid): %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Model)
		wantErr string
	}{
		{"nil detector", func(m *Model) { m.Detector = nil }, "missing detector spec"},
		{"no classes", func(m *Model) { m.Detector.Classes = nil }, "no classes"},
		{"zero width", func(m *Model) { m.Input.Width = 0 }, "invalid input size"},
		{"normalize true", func(m *Model) { m.Input.Normalize = true }, "normalize must be false"},
		{"non-zero mean", func(m *Model) { m.Input.Mean = [3]float32{0.5, 0, 0} }, "mean must be zero"},
		{"non-zero std", func(m *Model) { m.Input.Std = [3]float32{0, 1, 0} }, "std must be zero"},
		{"unsupported resize", func(m *Model) { m.Input.Resize = "letterbox" }, "unsupported resize mode"},
		{"non-zero crop_pct", func(m *Model) { m.Input.CropPct = 1.0 }, "crop_pct must be 0"},
		{"classifier interpolation", func(m *Model) { m.Input.Interpolation = InterpolationBicubic }, "interpolation must be empty or"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			spec := *base.Detector
			m.Detector = &spec
			tc.mutate(&m)
			if _, err := newDetector(m, pool); err == nil {
				t.Fatalf("newDetector() = nil error, want error containing %q", tc.wantErr)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("newDetector() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}

	if _, err := newDetector(base, nil); err == nil {
		t.Fatal("newDetector(nil pool) = nil error, want error")
	}
}

func TestBoxIoU(t *testing.T) {
	a := Box{X: 100, Y: 100, W: 20, H: 20}
	b := Box{X: 102, Y: 100, W: 20, H: 20}
	requireNear(t, "overlapping IoU", boxIoU(a, b), 360.0/440.0, 1e-6)
	requireNear(t, "disjoint IoU", boxIoU(a, Box{X: 300, Y: 300, W: 20, H: 20}), 0, 1e-6)
	requireNear(t, "identical IoU", boxIoU(a, a), 1, 1e-6)
}

func TestClampFloat(t *testing.T) {
	if got := clampFloat(-1, 0, 10); got != 0 {
		t.Errorf("clampFloat(-1) = %v, want 0", got)
	}
	if got := clampFloat(11, 0, 10); got != 10 {
		t.Errorf("clampFloat(11) = %v, want 10", got)
	}
	if got := clampFloat(5, 0, 10); got != 5 {
		t.Errorf("clampFloat(5) = %v, want 5", got)
	}
}

func TestSoftmaxNormalizesAcrossClasses(t *testing.T) {
	out, err := softmax([]float32{-1, -1, -1, -1})
	if err != nil {
		t.Fatalf("softmax: %v", err)
	}
	for i, v := range out {
		requireNear(t, fmt.Sprintf("out[%d]", i), v, 0.25, 1e-5)
	}
	if math.IsNaN(float64(out[0])) {
		t.Fatal("softmax returned NaN")
	}
}
