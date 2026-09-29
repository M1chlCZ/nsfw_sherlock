package engine

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// MaxInputDimension is the largest width or height accepted by Preprocess.
const MaxInputDimension = 8192

// ResizeMode selects how Preprocess maps a decoded image onto the model input.
type ResizeMode string

// ResizeStretch resizes to exactly Width x Height, without cropping or preserving aspect ratio.
const ResizeStretch ResizeMode = "stretch"

// Interpolation selects the resampling filter used by Preprocess.
const (
	// InterpolationBicubic selects Catmull-Rom resampling and is the default when Interpolation is empty.
	InterpolationBicubic = "bicubic"
	// InterpolationBilinear selects linear resampling.
	InterpolationBilinear = "bilinear"
)

// MinCropPct is the smallest accepted non-zero CropPct.
const MinCropPct float32 = 0.1

// InputSpec describes the tensor Preprocess builds from an image.
type InputSpec struct {
	Width     int
	Height    int
	Mean      [3]float32
	Std       [3]float32
	Normalize bool
	Resize    ResizeMode
	// CropPct selects center-crop preprocessing.
	CropPct float32
	// Interpolation selects the resampling filter.
	Interpolation string
}

// Limits bounds the size of an accepted image.
type Limits struct {
	MaxBytes  int64
	MaxPixels int64
}

var (
	// ErrImageTooLarge is returned when input exceeds Limits.
	ErrImageTooLarge = errors.New("engine: image too large")
	// ErrUnsupportedImage is returned when input cannot be decoded.
	ErrUnsupportedImage = errors.New("engine: unsupported or corrupt image")

	defaultLimits = Limits{MaxBytes: 20 << 20, MaxPixels: 50_000_000}
)

func decodeImage(data []byte, limits Limits) (image.Image, error) {
	if limits.MaxBytes > 0 && int64(len(data)) > limits.MaxBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrImageTooLarge, len(data))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedImage, err)
	}
	if limits.MaxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > limits.MaxPixels {
		return nil, fmt.Errorf("%w: %dx%d pixels", ErrImageTooLarge, cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedImage, err)
	}
	return img, nil
}

// DecodeReader reads an image from r, rejecting input larger than limits.MaxBytes.
func DecodeReader(r io.Reader, limits Limits) (image.Image, error) {
	if limits.MaxBytes <= 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("engine: read image: %w", err)
		}
		return decodeImage(data, limits)
	}
	data, err := io.ReadAll(io.LimitReader(r, limits.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("engine: read image: %w", err)
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrImageTooLarge, limits.MaxBytes)
	}
	return decodeImage(data, limits)
}

// Preprocess resizes img to spec.Width x spec.Height and returns an NCHW float32 tensor.
func Preprocess(img image.Image, spec InputSpec) ([]float32, error) {
	if img == nil {
		return nil, errors.New("engine: nil image")
	}
	if spec.Width <= 0 || spec.Height <= 0 {
		return nil, fmt.Errorf("engine: invalid input size %dx%d", spec.Width, spec.Height)
	}
	if spec.Width > MaxInputDimension || spec.Height > MaxInputDimension {
		return nil, fmt.Errorf("engine: input size %dx%d exceeds max dimension %d", spec.Width, spec.Height, MaxInputDimension)
	}
	if spec.Resize != "" && spec.Resize != ResizeStretch {
		return nil, fmt.Errorf("engine: unsupported resize mode %q", spec.Resize)
	}
	if math.IsNaN(float64(spec.CropPct)) || spec.CropPct < 0 || spec.CropPct > 1 || (spec.CropPct > 0 && spec.CropPct < MinCropPct) {
		return nil, fmt.Errorf("engine: invalid crop_pct %v: must be 0 or in [%v, 1]", spec.CropPct, MinCropPct)
	}
	if err := validateInterpolation(spec.Interpolation); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	if spec.Normalize {
		for c := 0; c < 3; c++ {
			mean, std := spec.Mean[c], spec.Std[c]
			if math.IsNaN(float64(mean)) || math.IsInf(float64(mean), 0) ||
				math.IsNaN(float64(std)) || math.IsInf(float64(std), 0) || std == 0 {
				return nil, fmt.Errorf("engine: invalid normalization parameters for channel %d", c)
			}
		}
	}

	var src *image.NRGBA
	if spec.CropPct > 0 {
		src = prepareSourceCrop(img, spec.Width, spec.Height, spec.CropPct, spec.Interpolation)
	} else {
		src = prepareSource(img, spec.Width, spec.Height, spec.Interpolation)
	}
	w, h := spec.Width, spec.Height

	out := make([]float32, 3*h*w)
	for y := 0; y < h; y++ {
		row := y * src.Stride
		for x := 0; x < w; x++ {
			i := row + x*4
			for c := 0; c < 3; c++ {
				v := float32(src.Pix[i+c]) / 255
				if spec.Normalize {
					v = (v - spec.Mean[c]) / spec.Std[c]
				}
				out[c*h*w+y*w+x] = v
			}
		}
	}
	return out, nil
}

func resizeInterpolator(interpolation string) draw.Interpolator {
	if interpolation == InterpolationBilinear {
		return draw.BiLinear
	}
	return draw.CatmullRom
}

func validateInterpolation(interpolation string) error {
	switch interpolation {
	case "", InterpolationBicubic, InterpolationBilinear:
		return nil
	default:
		return fmt.Errorf("unsupported interpolation %q", interpolation)
	}
}

func prepareSource(img image.Image, w, h int, interpolation string) *image.NRGBA {
	b := img.Bounds()
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		if nrgba, ok := img.(*image.NRGBA); ok && b.Min == (image.Point{}) && b.Dx() == w && b.Dy() == h {
			return nrgba
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		if b.Dx() == w && b.Dy() == h {
			draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		} else {
			resizeInterpolator(interpolation).Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		}
		return dst
	}
	src := flattenOpaque(img)
	if src.Bounds().Dx() == w && src.Bounds().Dy() == h {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	resizeInterpolator(interpolation).Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

const (
	cropResizeAreaFactor = 16
	cropResizeDimFactor  = 4
)

func cropResizeTooLarge(resizeW, resizeH, width, height int) bool {
	if resizeW <= 0 || resizeH <= 0 {
		return false
	}
	if limit := cropResizeDimFactor * max(width, height); resizeW > limit || resizeH > limit {
		return true
	}
	return int64(resizeW)*int64(resizeH) > cropResizeAreaFactor*int64(width)*int64(height)
}

func prepareSourceCrop(img image.Image, w, h int, cropPct float32, interpolation string) *image.NRGBA {
	resizeW, resizeH, cropX, cropY := cropGeometry(img.Bounds().Dx(), img.Bounds().Dy(), w, h, cropPct)
	if resizeW == w && resizeH == h && cropX == 0 && cropY == 0 {
		return prepareSource(img, w, h, interpolation)
	}
	if cropResizeTooLarge(resizeW, resizeH, w, h) {
		return prepareSourceCropROI(img, w, h, interpolation, resizeW, resizeH, cropX, cropY)
	}
	resized := prepareSource(img, resizeW, resizeH, interpolation)
	if cropX == 0 && cropY == 0 {
		return resized
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), resized, image.Point{X: cropX, Y: cropY}, draw.Src)
	return dst
}

func prepareSourceCropROI(img image.Image, w, h int, interpolation string, resizeW, resizeH, cropX, cropY int) *image.NRGBA {
	roi := sourceCropROI(img.Bounds(), resizeW, resizeH, cropX, cropY, w, h)
	if roi.Empty() {
		return prepareSource(img, w, h, interpolation)
	}
	return prepareSource(cropSubImage(img, roi), w, h, interpolation)
}

func sourceCropROI(b image.Rectangle, resizeW, resizeH, cropX, cropY, w, h int) image.Rectangle {
	if b.Dx() <= 0 || b.Dy() <= 0 || resizeW <= 0 || resizeH <= 0 || w <= 0 || h <= 0 {
		return image.Rectangle{}
	}
	scaleX := float64(b.Dx()) / float64(resizeW)
	scaleY := float64(b.Dy()) / float64(resizeH)
	roi := image.Rect(
		int(math.Floor(float64(cropX)*scaleX)),
		int(math.Floor(float64(cropY)*scaleY)),
		int(math.Ceil(float64(cropX+w)*scaleX)),
		int(math.Ceil(float64(cropY+h)*scaleY)),
	).Add(b.Min).Intersect(b)
	if roi.Dx() <= 0 || roi.Dy() <= 0 {
		return image.Rectangle{}
	}
	return roi
}

func cropSubImage(img image.Image, r image.Rectangle) image.Image {
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return s.SubImage(r)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, draw.Src)
	return dst
}

func cropGeometry(srcW, srcH, width, height int, cropPct float32) (resizeW, resizeH, cropX, cropY int) {
	if cropPct <= 0 {
		return width, height, 0, 0
	}
	if srcW <= 0 || srcH <= 0 {
		return width, height, 0, 0
	}
	target := max(1, int(float64(width)/float64(cropPct)))
	if srcW <= srcH {
		resizeW = target
		resizeH = max(1, int(float64(target)*float64(srcH)/float64(srcW)))
	} else {
		resizeH = target
		resizeW = max(1, int(float64(target)*float64(srcW)/float64(srcH)))
	}
	return resizeW, resizeH,
		roundHalfEven(float64(resizeW-width) / 2),
		roundHalfEven(float64(resizeH-height) / 2)
}

func flattenOpaque(img image.Image) *image.NRGBA {
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	if nrgba, ok := img.(*image.NRGBA); ok {
		rowLen := b.Dx() * 4
		for y := 0; y < b.Dy(); y++ {
			srcOff := nrgba.PixOffset(b.Min.X, b.Min.Y+y)
			dstOff := y * dst.Stride
			copy(dst.Pix[dstOff:dstOff+rowLen], nrgba.Pix[srcOff:srcOff+rowLen])
			for x := 3; x < rowLen; x += 4 {
				dst.Pix[dstOff+x] = 0xff
			}
		}
		return dst
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			i := y*dst.Stride + x*4
			dst.Pix[i+0] = c.R
			dst.Pix[i+1] = c.G
			dst.Pix[i+2] = c.B
			dst.Pix[i+3] = 0xff
		}
	}
	return dst
}
