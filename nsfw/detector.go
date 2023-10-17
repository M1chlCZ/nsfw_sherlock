package nsfw

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/galeone/tensorflow/tensorflow/go/op"
	"github.com/photoprism/photoprism/pkg/fs"
	"golang.org/x/image/webp"
	"image/png"
	"log"
	"nsfw_sherlock/utils"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tf "github.com/galeone/tensorflow/tensorflow/go"
	_ "github.com/galeone/tfgo"
)

// Detector uses TensorFlow to label drawing, hentai, neutral, porn and sexy images.
type Detector struct {
	model     *tf.SavedModel
	modelPath string
	modelTags []string
	labels    []string
	mutex     sync.Mutex
}

// New returns a new detector instance.
func New(modelPath string) *Detector {
	return &Detector{modelPath: modelPath, modelTags: []string{"serve"}}
}

// File returns matching labels for a jpeg media file.
func (t *Detector) File(filename string) (result Labels, err error) {
	utils.ReportMessage(fmt.Sprintf("nsfw: processing type %s", fs.MimeType(filename)))

	imageBuffer, err := os.ReadFile(filename)

	if err != nil {
		return result, err
	}

	return t.Labels(imageBuffer, strings.Split(fs.MimeType(filename), "/")[1])
}

// Labels returns matching labels for a jpeg media string.
func (t *Detector) Labels(img []byte, filetype string) (result Labels, err error) {
	if err := t.loadModel(); err != nil {
		return result, err
	}

	// Make tensor
	tensor, err := createTensorFromImage(img, filetype)

	if err != nil {
		return result, fmt.Errorf("nsfw: %s", err)
	}

	// Run inference
	output, err := t.model.Session.Run(
		map[tf.Output]*tf.Tensor{
			t.model.Graph.Operation("serving_default_input").Output(0): tensor,
		},
		[]tf.Output{
			t.model.Graph.Operation("StatefulPartitionedCall").Output(0),
		},
		nil)

	if err != nil {
		return result, fmt.Errorf("nsfw: %s (run inference)", err.Error())
	}

	if len(output) < 1 {
		return result, fmt.Errorf("nsfw: inference failed, no output")
	}

	// Return best labels
	result = t.getLabels(output[0].Value().([][]float32)[0])

	log.Println(fmt.Sprintf("nsfw: image classified as %+v", result))

	return result, nil
}

func (t *Detector) loadLabels(path string) error {
	modelLabels := path + "/labels.txt"

	log.Println(fmt.Sprintf("nsfw: loading labels from labels.txt"))

	// Load labels
	f, err := os.Open(modelLabels)

	if err != nil {
		return err
	}

	defer f.Close()

	scanner := bufio.NewScanner(f)

	// Labels are separated by newlines
	for scanner.Scan() {
		t.labels = append(t.labels, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func (t *Detector) loadModel() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.model != nil {
		// Already loaded
		return nil
	}

	log.Println(fmt.Sprintf("nsfw: loading %s", filepath.Base(t.modelPath)))

	// Load model
	model, err := tf.LoadSavedModel(t.modelPath, t.modelTags, nil)

	if err != nil {
		return err
	}

	t.model = model

	return t.loadLabels(t.modelPath)
}

func (t *Detector) getLabels(p []float32) Labels {
	return Labels{
		Drawings: p[0],
		Hentai:   p[1],
		Neutral:  p[2],
		Porn:     p[3],
		Sexy:     p[4],
	}
}

func transformImageGraph(imageFormat string) (graph *tf.Graph, input, output tf.Output, err error) {
	const (
		H, W  = 224, 224
		Mean  = float32(117)
		Scale = float32(1)
	)
	s := op.NewScope()
	input = op.Placeholder(s, tf.String)
	// Decode PNG or JPEG
	var decode tf.Output
	if imageFormat == "png" {
		decode = op.DecodePng(s, input, op.DecodePngChannels(3))
	} else if imageFormat == "gif" {
		decode = op.DecodeGif(s, input)
	} else if imageFormat == "bmp" {
		decode = op.DecodeBmp(s, input, op.DecodeBmpChannels(3))
	} else if imageFormat == "jpeg" || imageFormat == "jpg" {
		decode = op.DecodeJpeg(s, input, op.DecodeJpegChannels(3))
	} else {
		return nil, tf.Output{}, tf.Output{}, fmt.Errorf("image format not supported: %s", imageFormat)
	}
	// Div and Sub perform (value-Mean)/Scale for each pixel
	output = op.Div(s,
		op.Sub(s,
			// Resize to 224x224 with bilinear interpolation
			op.ResizeBilinear(s,
				// Create a batch containing a single image
				op.ExpandDims(s,
					// Use decoded pixel values
					op.Cast(s, decode, tf.Float),
					op.Const(s.SubScope("make_batch"), int32(0))),
				op.Const(s.SubScope("size"), []int32{H, W})),
			op.Const(s.SubScope("mean"), Mean)),
		op.Const(s.SubScope("scale"), Scale))
	graph, err = s.Finalize()
	return graph, input, output, err
}

func createTensorFromImage(image []byte, imageFormat string) (*tf.Tensor, error) {
	var err error
	if imageFormat == "webp" {
		image, err = webpToPng(image)
		if err != nil {
			return nil, err
		}
		imageFormat = "png"
	}
	tensor, err := tf.NewTensor(string(image))
	if err != nil {
		return nil, err
	}
	graph, input, output, err := transformImageGraph(imageFormat)
	if err != nil {
		return nil, err
	}
	session, err := tf.NewSession(graph, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	normalized, err := session.Run(
		map[tf.Output]*tf.Tensor{input: tensor},
		[]tf.Output{output},
		nil)
	if err != nil {
		return nil, err
	}
	return normalized[0], nil
}

func webpToPng(image []byte) ([]byte, error) {
	img, err := webp.Decode(bytes.NewReader(image))
	if err != nil {
		return image, err
	}
	buf := new(bytes.Buffer)
	err = png.Encode(buf, img)
	if err != nil {
		return image, err
	}
	return buf.Bytes(), nil
}
