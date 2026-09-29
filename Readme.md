# NSFW Sherlock

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://www.gnu.org/licenses/agpl-3.0.html)
[![Go Report Card](https://goreportcard.com/badge/github.com/M1chlCZ/nsfw_sherlock)](https://goreportcard.com/report/github.com/M1chlCZ/nsfw_sherlock)
[![Docker Hub](https://img.shields.io/docker/v/m1chl/nsfw-sherlock/latest?color=green&label=Docker%20Hub&style=flat)](https://hub.docker.com/repository/docker/m1chl/nsfw-sherlock/general)

Image classification API for NSFW content, served over HTTP and gRPC from a
single Go binary. Inference runs on ONNX Runtime; TensorFlow is not involved
anymore. The engine reads a manifest of ONNX models, verifies their SHA-256
digests, runs the classifiers and region detectors, and combines the results
into a verdict. When built with the `ocr` tag, it also checks text rendered in
the image with Tesseract.

## Quickstart

### Docker

    docker run -e APP_ENV=web -p 4000:4000 m1chl/nsfw-sherlock
    curl http://localhost:4000/ping

`APP_ENV=web` is the default; set `APP_ENV=grpc` to serve gRPC instead. The
deprecated `APP_ENV=http` still selects the HTTP server, but new deployments
should use `web`. The models are baked into the image. Check the model licences
below before commercial use.

### From source

    git clone https://github.com/M1chlCZ/nsfw_sherlock
    cd nsfw_sherlock
    scripts/fetch-models.sh --ort-only

    # Once the models-v1 release exists, download and verify every model:
    scripts/fetch-models.sh

    # Or export the two classifier models locally (pulls several GB of
    # Python dependencies):
    python3 -m venv .venv-export
    .venv-export/bin/pip install -r scripts/requirements-export.txt
    .venv-export/bin/python scripts/export-models.py --out assets/models

    go build -tags ocr .
    ./nsfw_sherlock

`scripts/fetch-models.sh --ort-only` installs the ONNX Runtime shared library
into `./lib`. Without `--ort-only`, the same script downloads the models
declared in `models/manifest.json` into `./assets/models` and verifies each
digest. `-tags ocr` compiles Tesseract-backed text detection; omit it for a
binary that never sets `nsfwText` and does not need Tesseract at all
(`go build .`). `MODELS_DIR` and `ORT_LIB` can point at other locations.
`--ort-only` verifies the runtime archive against a pinned SHA-256 digest before extraction; pass `--ort-sha256 HEX` for versions without a pinned digest.

## API

### HTTP

`GET /ping`:

```json
{"status":"OK"}
```

`POST /pic/check` request:

```json
{"base64":"<base64-encoded image>","filename":"image.jpg"}
```

`POST /pic/check` response:

```json
{"status":"ok","message":"success","nsfwText":false,"nsfwPic":false}
```

`POST /pic/labels` takes the same request and returns the legacy label scores
as probabilities:

```json
{
  "status": "ok",
  "message": "success",
  "drawings": 0.01,
  "hentai": 0.0,
  "neutral": 0.98,
  "porn": 0.0,
  "sexy": 0.01,
  "nsfwText": false
}
```

`POST /pic/analyze` takes the same request and returns the full analysis:
verdict (`sfw`, `suggestive` or `explicit`), the scalar `nsfw` score, the
legacy labels, per-model classifier scores, region detections and the loaded
models:

```json
{
  "status": "ok",
  "message": "success",
  "nsfwText": false,
  "verdict": "sfw",
  "nsfw": 0.02,
  "labels": {"drawings": 0.01, "hentai": 0.0, "neutral": 0.98, "porn": 0.0, "sexy": 0.01},
  "photo": {"model": "photo-freepik", "scores": {"neutral": 0.98, "low": 0.02, "medium": 0.0, "high": 0.0}},
  "anime": {"model": "anime-caformer", "scores": {"safe": 0.99, "r15": 0.01, "r18": 0.0}},
  "detections": [],
  "models": [
    {"id": "photo-freepik", "kind": "classifier", "sha256": "43f3aafc..."},
    {"id": "anime-caformer", "kind": "classifier", "sha256": "fd5fdea9..."},
    {"id": "regions-nudenet-320", "kind": "detector", "sha256": "c15d8273..."}
  ],
  "elapsedMs": 87
}
```

Errors keep the legacy body: `{"errorMessage":"...","status":"FAIL","hasError":true}`.

### gRPC

Set `APP_ENV=grpc` and use the service definition in `proto/nsfw.proto`. The
`NSFW` service exposes:

- `Detect(NSFWRequest) returns (NSFWResponse)` with `nsfwPicture` and `nsfwText`
- `DetectLabels(NSFWLabelsRequest) returns (NSFWLabels)` with the five legacy labels
- `Analyze(NSFWRequest) returns (Analysis)` with the full analysis

Regenerate the stubs with `make proto`.

## Profiles

`NSFW_PROFILE` selects a manifest profile; the default is `balanced`.

| Profile | Models | Trade-off |
| --- | --- | --- |
| `balanced` | `photo-freepik`, `anime-caformer`, `regions-nudenet-320` | default; best mix of accuracy and latency |
| `max` | `photo-freepik`, `anime-caformer`, `regions-nudenet-640` | detector runs at 640px; slower, catches smaller regions |
| `fast` | `photo-freepik`, `anime-mobilenet`, `regions-nudenet-320` | cheaper anime classifier, weaker on small inputs |
| `compat` | `photo-compat-vit5` | legacy five-label output only; no anime model or detectors |

`balanced`, `max` and `fast` map the EVA02 photo scores onto the legacy labels
and add photo/anime score maps plus region detections. `compat` returns the
ViT5 model's five labels directly for drop-in compatibility with old clients;
it uses a non-commercial model, so treat it as a migration path.

## Models

| Role | Upstream | License | Notes |
| --- | --- | --- | --- |
| photo (`balanced`, `max`, `fast`) | [Freepik/nsfw_image_detector](https://huggingface.co/Freepik/nsfw_image_detector) | MIT | EVA02-base at 448px, labels neutral/low/medium/high; required model |
| photo (`compat`) | [giacomoarienti/nsfw-classifier](https://huggingface.co/giacomoarienti/nsfw-classifier) | CC-BY-NC-ND-4.0 | ViT-base at 224px, legacy five labels. Non-commercial and no derivatives: the exported ONNX may not be redistributable or usable commercially |
| anime | [deepghs/anime_rating](https://huggingface.co/deepghs/anime_rating) | MIT | caformer at 384px or mobilenetv3 at 224px, labels safe/r15/r18 |
| regions | [notAI-tech/NudeNet](https://github.com/notAI-tech/NudeNet) | AGPL-3.0 | 18-class region detector at 320px or 640px. The GitHub repo is behind a login gate, so the manifest uses the Hugging Face mirrors [deepghs/nudenet_onnx](https://huggingface.co/deepghs/nudenet_onnx) and [Kalashnikov/NudeNet](https://huggingface.co/Kalashnikov/NudeNet) |

Model licences apply independently of the code licence. The NudeNet detectors
are AGPL-3.0 and the `compat` classifier is CC-BY-NC-ND-4.0; review both
before shipping a commercial product.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `APP_ENV` | `web` | `web` for HTTP, `grpc` for gRPC; `http` is a deprecated alias for `web` |
| `PORT` | `4000` | Listen port (1-65535) |
| `MODELS_DIR` | `./assets/models` | Directory holding the manifest model files |
| `MANIFEST` | `models/manifest.json` | Path to the model manifest file |
| `ORT_LIB` | auto-detected | Path to the ONNX Runtime shared library; otherwise `./lib` next to the binary is searched |
| `NSFW_PROFILE` | `balanced` | Manifest profile to load |
| `NSFW_ALLOW_DEGRADED` | `false` | Start even when a required model fails to load, turning the failure into a warning |
| `OCR_ENABLED` | `true` | Run text detection per request (requires the `ocr` build tag) |
| `BAD_WORDS_FILE` | auto-detected | Word list for OCR text; falls back to `bad_words.txt`, `/bad_words.txt`, then the embedded list |
| `MAX_IMAGE_BYTES` | `20971520` (20 MiB) | Largest accepted image |
| `MAX_IMAGE_PIXELS` | `50000000` | Largest decoded pixel count |
| `SESSION_POOL_SIZE` | `2` | ONNX sessions created per model |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `LOG_FORMAT` | `text` | `text` or `json` |

## OCR

Text detection is compiled in with the `ocr` build tag and uses Tesseract with
the English data. Build-time packages: `libtesseract-dev`, `libleptonica-dev`.
Runtime: `tesseract-ocr`, `tesseract-ocr-eng`.

Without the tag, or with `OCR_ENABLED=false`, the service starts normally but
always reports `nsfwText: false` and logs a warning. The word list comes from
`BAD_WORDS_FILE`; when unset, `bad_words.txt` in the working directory is
used, then `/bad_words.txt`, then the embedded fallback.

## Adding or upgrading a model

1. Edit `models/manifest.json`. Each entry needs `id`, `kind`
   (`classifier` or `detector`), `role`, `file`, `sha256`, `input_names`,
   `output_names`, `source` (`type`: `hf`, `release` or `local`, plus `url`),
   the `input` preprocessing spec (`width`, `height`, `mean`, `std`,
   `normalize`, `resize`, `crop_pct`, `interpolation`), `labels`,
   `output.activation` (`softmax` or `none`), `required`, `license` and
   `source_repo`. Detectors also need the `detector` block (`classes`,
   `score_threshold`, `iou_threshold`, `explicit_classes`,
   `suggestive_classes`). Add the model id to every profile that should load
   it.
2. Run `scripts/inspect-models.py` to confirm inputs, outputs and processor
   settings, and to dump a parity fixture into `testdata/parity/`.
3. Export the model. `scripts/export-models.py --out assets/models` covers the
   Hugging Face classifiers; `scripts/fetch-models.sh` downloads whatever the
   manifest declares and verifies the digests.
4. Run `PARITY_REQUIRE=1 go test ./engine -run TestParity` to check Go
   preprocessing and inference against the fixtures.
5. Benchmark with `go run ./cmd/bench -dir <labeled-dir> -profile balanced`,
   where `<labeled-dir>` contains one directory per class and `nsfw` is the
   positive class. Add `-json` for a machine-readable summary.

To re-export the classifier models in CI, run the `Export models` workflow
(default tag `models-v1`). It publishes the ONNX files and their `.sha256`
sidecars to the release the manifest already points at.

## Development

    go test ./... -count=1 -race
    go test -tags ocr ./textcheck/... -count=1 -race   # real Tesseract test
    PARITY_REQUIRE=1 go test ./engine -run TestParity  # needs models + ORT
    go run ./cmd/bench -dir <labeled-dir> -profile fast
    make proto                                          # protoc + Go plugins

CI runs `go vet`, both test suites, `govulncheck`, golangci-lint, then builds
the Docker image and smoke-tests `/pic/check` against the running container.
The parity fixtures under `testdata/parity` are generated by
`scripts/inspect-models.py`; regenerate them when a manifest entry changes.

## Migrating from the old version

- HTTP and gRPC response shapes are unchanged, including the error body
  (`errorMessage`, `status`, `hasError`).
- TensorFlow and its Go bindings are gone; models run through ONNX Runtime and
  are checked against the manifest digests at startup.
- The service no longer writes temporary files per request.
- `APP_ENV=http` now selects the HTTP server as a deprecated alias for `web`.
  Existing `docker run -e APP_ENV=http ...` commands keep working, but prefer
  `web` (the default) or `grpc`.

## Credits

- Models: [Freepik](https://huggingface.co/Freepik/nsfw_image_detector),
  [giacomoarienti](https://huggingface.co/giacomoarienti/nsfw-classifier),
  [deepghs](https://huggingface.co/deepghs/anime_rating) and
  [NudeNet](https://github.com/notAI-tech/NudeNet) (via the Hugging Face
  mirrors above).
- OCR: [gosseract](https://github.com/otiai10/gosseract) and Tesseract.
- Runtime: [onnxruntime_go](https://github.com/yalue/onnxruntime_go).
