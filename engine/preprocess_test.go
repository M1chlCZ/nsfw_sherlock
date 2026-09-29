package engine

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"runtime"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func makeLimits(maxBytes, maxPixels int64) Limits {
	return Limits{MaxBytes: maxBytes, MaxPixels: maxPixels}
}

func quadImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	return img
}

func quadPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, quadImage()); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeSniffsFormats(t *testing.T) {
	img := quadImage()

	var pngBuf, jpegBuf, gifBuf, bmpBuf, tiffBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	if err := gif.Encode(&gifBuf, img, nil); err != nil {
		t.Fatalf("encode GIF: %v", err)
	}
	if err := bmp.Encode(&bmpBuf, img); err != nil {
		t.Fatalf("encode BMP: %v", err)
	}
	if err := tiff.Encode(&tiffBuf, img, nil); err != nil {
		t.Fatalf("encode TIFF: %v", err)
	}
	webpData, err := os.ReadFile("../testdata/tiny.webp")
	if err != nil {
		t.Fatalf("read webp fixture: %v", err)
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"PNG", pngBuf.Bytes()},
		{"JPEG", jpegBuf.Bytes()},
		{"GIF", gifBuf.Bytes()},
		{"BMP", bmpBuf.Bytes()},
		{"TIFF", tiffBuf.Bytes()},
		{"WebP", webpData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeImage(tc.data, defaultLimits)
			if err != nil {
				t.Fatalf("decodeImage: %v", err)
			}
			if got == nil {
				t.Fatal("decodeImage returned nil image")
			}
			if b := got.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
				t.Fatalf("decoded empty bounds: %v", b)
			}
		})
	}
}

func TestDecodeRejectsGarbageAndHuge(t *testing.T) {
	t.Run("garbage", func(t *testing.T) {
		if _, err := decodeImage([]byte("not an image"), defaultLimits); !errors.Is(err, ErrUnsupportedImage) {
			t.Fatalf("decodeImage(garbage) = %v, want ErrUnsupportedImage", err)
		}
	})

	t.Run("max bytes", func(t *testing.T) {
		if _, err := decodeImage(quadPNG(t), makeLimits(8, 0)); !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("decodeImage(over MaxBytes) = %v, want ErrImageTooLarge", err)
		}
	})

	t.Run("max pixels", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 10, 10))
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode PNG: %v", err)
		}
		if _, err := decodeImage(buf.Bytes(), makeLimits(1<<20, 1)); !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("decodeImage(over MaxPixels) = %v, want ErrImageTooLarge", err)
		}
	})
}

func TestPreprocessShapeValues(t *testing.T) {
	spec := InputSpec{
		Width:     2,
		Height:    2,
		Mean:      [3]float32{0.5, 0.5, 0.5},
		Std:       [3]float32{0.5, 0.5, 0.5},
		Normalize: true,
	}
	out, err := Preprocess(quadImage(), spec)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if len(out) != 12 {
		t.Fatalf("len(out) = %d, want 12", len(out))
	}
	want := []float32{
		1, -1, -1, 1,
		-1, 1, -1, 1,
		-1, -1, 1, 1,
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %v, want %v (full: %v)", i, out[i], want[i], out)
		}
	}
}

func TestPreprocessStretchResize(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x, v := range []uint8{0, 85, 170, 255} {
		i := img.PixOffset(x, 0)
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 0xff
	}
	out, err := Preprocess(img, InputSpec{Width: 2, Height: 1})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("len(out) = %d, want 6", len(out))
	}
	for i, v := range out {
		if v <= 0 || v >= 1 {
			t.Errorf("out[%d] = %v, want strictly between 0 and 1", i, v)
		}
	}
	for x, want := range []float32{50.0 / 255, 205.0 / 255} {
		if diff := math.Abs(float64(out[x]) - float64(want)); diff > 0.02 {
			t.Errorf("out[%d] = %v, want ~%v (tolerance 0.02)", x, out[x], want)
		}
	}
	if out[0] >= out[1] {
		t.Errorf("gradient not increasing: %v", out[:2])
	}
	if out[0] != out[2] || out[2] != out[4] || out[1] != out[3] || out[3] != out[5] {
		t.Errorf("channels diverge for grayscale input: %v", out)
	}
}

func TestCropGeometry(t *testing.T) {
	cases := []struct {
		name                      string
		srcW, srcH, width, height int
		cropPct                   float32
		wantResizeW, wantResizeH  int
		wantCropX, wantCropY      int
	}{
		{"no crop", 448, 112, 224, 224, 0, 224, 224, 0, 0},
		{"full crop square source", 448, 112, 448, 448, 1.0, 1792, 448, 672, 0},
		{"no upscale needed", 4, 2, 2, 2, 1.0, 4, 2, 1, 0},
		{"integer axes", 8, 4, 4, 2, 1.0, 8, 4, 2, 1},
		{"mobilenet crop 0.875", 448, 112, 224, 224, 0.875, 1024, 256, 400, 16},
		{"non-multiple scale", 300, 100, 224, 224, 0.875, 768, 256, 272, 16},
		{"odd difference rounds half to even", 5, 3, 2, 2, 1.0, 3, 2, 0, 0},
		{"window taller than resized pads", 3, 5, 4, 8, 1.0, 4, 6, 0, -1},
		{"long edge 1920x1080 truncates", 1920, 1080, 384, 384, 1.0, 682, 384, 149, 0},
		{"long edge 1000x333 truncates", 1000, 333, 384, 384, 1.0, 1153, 384, 384, 0},
		{"tall 333x1000 truncates", 333, 1000, 384, 384, 1.0, 384, 1153, 0, 384},
		{"odd primes 997x613", 997, 613, 384, 384, 1.0, 624, 384, 120, 0},
		{"long edge 1920x1080 at crop 0.875", 1920, 1080, 224, 224, 0.875, 455, 256, 116, 16},
		{"long edge 1000x333 at crop 0.875", 1000, 333, 224, 224, 0.875, 768, 256, 272, 16},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resizeW, resizeH, cropX, cropY := cropGeometry(c.srcW, c.srcH, c.width, c.height, c.cropPct)
			if resizeW != c.wantResizeW || resizeH != c.wantResizeH {
				t.Errorf("cropGeometry(%d, %d, %d, %d, %v) resize = %dx%d, want %dx%d",
					c.srcW, c.srcH, c.width, c.height, c.cropPct, resizeW, resizeH, c.wantResizeW, c.wantResizeH)
			}
			if cropX != c.wantCropX || cropY != c.wantCropY {
				t.Errorf("cropGeometry(%d, %d, %d, %d, %v) crop = (%d, %d), want (%d, %d)",
					c.srcW, c.srcH, c.width, c.height, c.cropPct, cropX, cropY, c.wantCropX, c.wantCropY)
			}
		})
	}
}

func TestCropResizeTooLarge(t *testing.T) {
	cases := []struct {
		name             string
		resizeW, resizeH int
		width, height    int
		want             bool
	}{
		{"caformer 65535x1", 25165440, 384, 384, 384, true},
		{"mobilenet 65535x1", 16776960, 256, 224, 224, true},
		{"caformer 4000x1", 1536000, 384, 384, 384, true},
		{"exact target square", 384, 384, 384, 384, false},
		{"parity 1920x1080 crop 1.0", 682, 384, 384, 384, false},
		{"parity 1000x333 crop 1.0", 1153, 384, 384, 384, false},
		{"parity caformer 512x384", 512, 384, 384, 384, false},
		{"parity mobilenet 512x384", 341, 256, 224, 224, false},
		{"aspect four mobilenet", 1024, 256, 224, 224, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cropResizeTooLarge(c.resizeW, c.resizeH, c.width, c.height); got != c.want {
				t.Errorf("cropResizeTooLarge(%d, %d, %d, %d) = %v, want %v",
					c.resizeW, c.resizeH, c.width, c.height, got, c.want)
			}
		})
	}
}

func TestPreprocessExtremeAspectCrop(t *testing.T) {
	const maxAllocBytes = 256 << 20
	cases := []struct {
		name          string
		srcW, srcH    int
		width, height int
		cropPct       float32
		whiteX        int
	}{
		{"caformer 17x1", 17, 1, 384, 384, 1.0, 8},
		{"caformer 65535x1", 65535, 1, 384, 384, 1.0, 32767},
		{"mobilenet 65535x1", 65535, 1, 224, 224, 0.875, 32767},
		{"caformer 4000x1", 4000, 1, 384, 384, 1.0, 1999},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, c.srcW, c.srcH))
			for y := range c.srcH {
				for x := range c.srcW {
					i := img.PixOffset(x, y)
					img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0, 0, 0, 0xff
				}
			}
			for x := c.whiteX; x <= c.whiteX+1 && x < c.srcW; x++ {
				i := img.PixOffset(x, 0)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 255, 255, 0xff
			}

			spec := InputSpec{Width: c.width, Height: c.height, CropPct: c.cropPct}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out, err := Preprocess(img, spec)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatalf("Preprocess: %v", err)
			}
			delta := after.TotalAlloc - before.TotalAlloc
			if delta > maxAllocBytes {
				t.Errorf("Preprocess allocated %d bytes, want < %d", delta, maxAllocBytes)
			} else {
				t.Logf("Preprocess allocated %d bytes (bound %d)", delta, maxAllocBytes)
			}
			if len(out) != 3*c.width*c.height {
				t.Fatalf("len(out) = %d, want %d", len(out), 3*c.width*c.height)
			}
			for i, v := range out {
				if math.Abs(float64(v)-1) > 0.01 {
					t.Fatalf("out[%d] = %v, want ~1 from the centered source pixels", i, v)
				}
			}
		})
	}
}

func grayRows(rows ...[]uint8) *image.NRGBA {
	height := len(rows)
	width := len(rows[0])
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y, row := range rows {
		for x, v := range row {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 0xff
		}
	}
	return img
}

func TestPreprocessCenterCrop(t *testing.T) {
	img := grayRows(
		[]uint8{0, 64, 128, 192},
		[]uint8{16, 80, 144, 208},
	)
	out, err := Preprocess(img, InputSpec{Width: 2, Height: 2, CropPct: 1.0})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	want := []float32{
		64.0 / 255, 128.0 / 255,
		80.0 / 255, 144.0 / 255,
		64.0 / 255, 128.0 / 255,
		80.0 / 255, 144.0 / 255,
		64.0 / 255, 128.0 / 255,
		80.0 / 255, 144.0 / 255,
	}
	if len(out) != len(want) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %v, want %v (full: %v)", i, out[i], want[i], out)
		}
	}
}

func TestPreprocessCenterCropOffsets(t *testing.T) {
	img := grayRows(
		[]uint8{0, 1, 2, 3, 4, 5, 6, 7},
		[]uint8{10, 11, 12, 13, 14, 15, 16, 17},
		[]uint8{20, 21, 22, 23, 24, 25, 26, 27},
		[]uint8{30, 31, 32, 33, 34, 35, 36, 37},
	)
	out, err := Preprocess(img, InputSpec{Width: 4, Height: 2, CropPct: 1.0})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	want := []float32{
		12.0 / 255, 13.0 / 255, 14.0 / 255, 15.0 / 255,
		22.0 / 255, 23.0 / 255, 24.0 / 255, 25.0 / 255,
		12.0 / 255, 13.0 / 255, 14.0 / 255, 15.0 / 255,
		22.0 / 255, 23.0 / 255, 24.0 / 255, 25.0 / 255,
		12.0 / 255, 13.0 / 255, 14.0 / 255, 15.0 / 255,
		22.0 / 255, 23.0 / 255, 24.0 / 255, 25.0 / 255,
	}
	if len(out) != len(want) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %v, want %v (full: %v)", i, out[i], want[i], out)
		}
	}
}

func TestPreprocessCenterCropPadding(t *testing.T) {
	t.Run("scale one window taller than the source", func(t *testing.T) {
		img := grayRows(
			[]uint8{10, 20, 30, 40},
			[]uint8{50, 60, 70, 80},
			[]uint8{90, 100, 110, 120},
			[]uint8{130, 140, 150, 160},
		)
		out, err := Preprocess(img, InputSpec{Width: 4, Height: 8, CropPct: 1.0})
		if err != nil {
			t.Fatalf("Preprocess: %v", err)
		}
		zeros := []float32{0, 0, 0, 0}
		plane := append([]float32{}, zeros...)
		plane = append(plane, zeros...)
		for _, values := range [][]float32{
			{10, 20, 30, 40},
			{50, 60, 70, 80},
			{90, 100, 110, 120},
			{130, 140, 150, 160},
		} {
			for _, v := range values {
				plane = append(plane, v/255)
			}
		}
		plane = append(plane, zeros...)
		plane = append(plane, zeros...)
		want := append([]float32{}, plane...)
		want = append(want, plane...)
		want = append(want, plane...)
		if len(out) != len(want) {
			t.Fatalf("len(out) = %d, want %d", len(out), len(want))
		}
		for i := range want {
			if out[i] != want[i] {
				t.Errorf("out[%d] = %v, want %v (full: %v)", i, out[i], want[i], out)
			}
		}
	})

	t.Run("review geometry 3x5 source into 4x8 window", func(t *testing.T) {
		const r, g, b = 200, 100, 50
		img := image.NewNRGBA(image.Rect(0, 0, 3, 5))
		for y := range 5 {
			for x := range 3 {
				i := img.PixOffset(x, y)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 0xff
			}
		}
		out, err := Preprocess(img, InputSpec{Width: 4, Height: 8, CropPct: 1.0})
		if err != nil {
			t.Fatalf("Preprocess: %v", err)
		}
		if len(out) != 3*4*8 {
			t.Fatalf("len(out) = %d, want %d", len(out), 3*4*8)
		}
		planeLen := 4 * 8
		for c, value := range [3]float32{r, g, b} {
			plane := out[c*planeLen : (c+1)*planeLen]
			for _, i := range []int{0, 1, 2, 3, 28, 29, 30, 31} {
				if plane[i] != 0 {
					t.Errorf("channel %d padded pixel [%d] = %v, want 0", c, i, plane[i])
				}
			}
			for i := 4; i < 28; i++ {
				if plane[i] != value/255 {
					t.Errorf("channel %d interior pixel [%d] = %v, want %v", c, i, plane[i], value/255)
				}
			}
		}
	})
}

func TestPreprocessCropPctValidation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for _, cropPct := range []float32{-0.1, 1.5, 0.05, float32(math.NaN()), float32(math.Inf(1))} {
		if _, err := Preprocess(img, InputSpec{Width: 2, Height: 2, CropPct: cropPct}); err == nil {
			t.Errorf("Preprocess(crop_pct=%v) = nil error, want error", cropPct)
		}
	}
	for _, cropPct := range []float32{0, 0.1, 1.0, 0.875} {
		if _, err := Preprocess(img, InputSpec{Width: 2, Height: 2, CropPct: cropPct}); err != nil {
			t.Errorf("Preprocess(crop_pct=%v) error = %v, want nil", cropPct, err)
		}
	}
}

func TestPreprocessInterpolationExact(t *testing.T) {
	wantRow := []float32{0, 64.0 / 255, 191.0 / 255, 1}

	t.Run("2x1 to 4x1", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
		copy(img.Pix, []uint8{0, 0, 0, 0xff, 255, 255, 255, 0xff})
		out, err := Preprocess(img, InputSpec{Width: 4, Height: 1, Interpolation: InterpolationBilinear})
		if err != nil {
			t.Fatalf("Preprocess: %v", err)
		}
		want := append(append(append([]float32{}, wantRow...), wantRow...), wantRow...)
		if len(out) != len(want) {
			t.Fatalf("len(out) = %d, want %d", len(out), len(want))
		}
		for i := range want {
			if out[i] != want[i] {
				t.Errorf("out[%d] = %v, want %v (full: %v)", i, out[i], want[i], out)
			}
		}
	})

	t.Run("2x2 to 4x4", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		copy(img.Pix, []uint8{
			0, 0, 0, 0xff, 255, 255, 255, 0xff,
			255, 255, 255, 0xff, 0, 0, 0, 0xff,
		})
		out, err := Preprocess(img, InputSpec{Width: 4, Height: 4, Interpolation: InterpolationBilinear})
		if err != nil {
			t.Fatalf("Preprocess: %v", err)
		}
		firstRow := []float32{0, 64.0 / 255, 191.0 / 255, 1}
		for c := range 3 {
			plane := out[c*16 : (c+1)*16]
			for x := range firstRow {
				if plane[x] != firstRow[x] {
					t.Errorf("channel %d first row [%d] = %v, want %v", c, x, plane[x], firstRow[x])
				}
			}
		}
	})
}

func TestPreprocessInterpolationValidation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for _, interpolation := range []string{"", InterpolationBicubic, InterpolationBilinear} {
		if _, err := Preprocess(img, InputSpec{Width: 2, Height: 2, Interpolation: interpolation}); err != nil {
			t.Errorf("Preprocess(interpolation=%q) error = %v, want nil", interpolation, err)
		}
	}
	if _, err := Preprocess(img, InputSpec{Width: 2, Height: 2, Interpolation: "nearest"}); err == nil {
		t.Error("Preprocess(interpolation=nearest) = nil error, want error")
	}
}

func TestPreprocessNoNormalize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	spec := InputSpec{
		Width:     1,
		Height:    1,
		Mean:      [3]float32{0.5, 0.5, 0.5},
		Std:       [3]float32{0.5, 0.5, 0.5},
		Normalize: false,
	}
	out, err := Preprocess(img, spec)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	want := []float32{1, 0, 0}
	if len(out) != len(want) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %v, want %v", i, out[i], want[i])
		}
	}
}

func TestPreprocessUnsupportedOrCorrupt(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, quadImage(), nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	data := buf.Bytes()
	if _, err := decodeImage(data[:len(data)/2], defaultLimits); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("decodeImage(truncated JPEG) = %v, want ErrUnsupportedImage", err)
	}
}

func TestDecodeReaderLimit(t *testing.T) {
	img, err := DecodeReader(bytes.NewReader(quadPNG(t)), defaultLimits)
	if err != nil {
		t.Fatalf("DecodeReader(valid PNG): %v", err)
	}
	if img == nil {
		t.Fatal("DecodeReader returned nil image")
	}

	big := bytes.Repeat([]byte{0xff}, 64)
	if _, err := DecodeReader(bytes.NewReader(big), makeLimits(32, 0)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("DecodeReader(over MaxBytes) = %v, want ErrImageTooLarge", err)
	}
}

func TestPreprocessAlphaFlattening(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	set := func(x int, r, g, b, a uint8) {
		i := img.PixOffset(x, 0)
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, a
	}
	set(0, 0, 255, 0, 0)
	set(1, 0, 255, 0, 0)
	set(2, 0, 0, 0, 255)
	set(3, 0, 0, 0, 255)

	out, err := Preprocess(img, InputSpec{Width: 2, Height: 1})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("len(out) = %d, want 6", len(out))
	}
	if out[2] < 0.85 {
		t.Errorf("out[2] = %v, want stored green of transparent pixels preserved", out[2])
	}
	if out[3] > 0.15 {
		t.Errorf("out[3] = %v, want opaque black neighbors to stay dark", out[3])
	}
	for _, i := range []int{0, 1, 4, 5} {
		if diff := math.Abs(float64(out[i])); diff > 0.02 {
			t.Errorf("out[%d] = %v, want ~0 red/blue", i, out[i])
		}
	}
}

func TestPrepareSourcePaths(t *testing.T) {
	t.Run("opaque nrgba reused", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
		copy(img.Pix, []uint8{255, 0, 0, 255, 0, 255, 0, 255})
		if got := prepareSource(img, 2, 1, InterpolationBicubic); got != img {
			t.Error("opaque NRGBA at target size was copied/flattened, want original image")
		}
	})

	t.Run("opaque nrgba resized", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
		for x, v := range []uint8{0, 85, 170, 255} {
			i := img.PixOffset(x, 0)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 0xff
		}
		got := prepareSource(img, 2, 1, InterpolationBicubic)
		if got == img {
			t.Fatal("resized source must be a new image")
		}
		if got.Pix[0] != 50 || got.Pix[4] != 205 {
			t.Errorf("resized pixel = %d %d, want 50 205", got.Pix[0], got.Pix[4])
		}
	})

	t.Run("non-opaque flattened", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
		copy(img.Pix, []uint8{0, 255, 0, 0, 0, 0, 0, 255})
		got := prepareSource(img, 2, 1, InterpolationBicubic)
		if got == img {
			t.Fatal("non-opaque source was not flattened")
		}
		if got.Pix[0] != 0 || got.Pix[1] != 255 || got.Pix[2] != 0 || got.Pix[3] != 255 {
			t.Errorf("transparent pixel = %v, want stored green and alpha 255", got.Pix[:4])
		}
		if got.Pix[7] != 255 {
			t.Errorf("opaque pixel alpha = %d, want 255", got.Pix[7])
		}
	})
}

func TestPreprocessRejectsOversizedDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	if _, err := Preprocess(img, InputSpec{Width: MaxInputDimension + 1, Height: 1}); err == nil {
		t.Fatal("Preprocess(width over MaxInputDimension) = nil error, want error")
	}
	if _, err := Preprocess(img, InputSpec{Width: 1, Height: MaxInputDimension + 1}); err == nil {
		t.Fatal("Preprocess(height over MaxInputDimension) = nil error, want error")
	}
	out, err := Preprocess(img, InputSpec{Width: MaxInputDimension, Height: 1})
	if err != nil {
		t.Fatalf("Preprocess(dimension at MaxInputDimension): %v", err)
	}
	if len(out) != 3*MaxInputDimension {
		t.Fatalf("len(out) = %d, want %d", len(out), 3*MaxInputDimension)
	}
}

func TestPreprocessRejectsNonFiniteParams(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	base := InputSpec{
		Width:     1,
		Height:    1,
		Normalize: true,
		Std:       [3]float32{1, 1, 1},
	}

	nan := base
	nan.Mean[1] = float32(math.NaN())
	if _, err := Preprocess(img, nan); err == nil {
		t.Fatal("Preprocess(NaN mean) = nil error, want error")
	}

	inf := base
	inf.Std[2] = float32(math.Inf(1))
	if _, err := Preprocess(img, inf); err == nil {
		t.Fatal("Preprocess(Inf std) = nil error, want error")
	}

	zero := base
	zero.Std[0] = 0
	if _, err := Preprocess(img, zero); err == nil {
		t.Fatal("Preprocess(zero std) = nil error, want error")
	}
}

func TestPreprocessPicSmoke(t *testing.T) {
	data, err := os.ReadFile("../testdata/pic.jpg")
	if err != nil {
		t.Fatalf("read pic.jpg: %v", err)
	}
	img, err := decodeImage(data, defaultLimits)
	if err != nil {
		t.Fatalf("decodeImage(pic.jpg): %v", err)
	}
	spec := InputSpec{
		Width:     224,
		Height:    224,
		Mean:      [3]float32{0.485, 0.456, 0.406},
		Std:       [3]float32{0.229, 0.224, 0.225},
		Normalize: true,
	}
	out, err := Preprocess(img, spec)
	if err != nil {
		t.Fatalf("Preprocess(pic.jpg): %v", err)
	}
	if len(out) != 3*224*224 {
		t.Fatalf("len(out) = %d, want %d", len(out), 3*224*224)
	}
	for i, v := range out {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("out[%d] = %v, want finite", i, v)
		}
	}
}
