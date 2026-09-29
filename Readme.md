# NSFW Sherlock

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://www.gnu.org/licenses/agpl-3.0.html)
[![Go Report Card](https://goreportcard.com/badge/github.com/M1chlCZ/nsfw_sherlock)](https://goreportcard.com/report/github.com/M1chlCZ/nsfw_sherlock)
[![Docker Hub](https://img.shields.io/docker/v/m1chl/nsfw-sherlock/latest?color=green&label=Docker%20Hub&style=flat)](https://hub.docker.com/repository/docker/m1chl/nsfw-sherlock/general)

An image classification API for NSFW content, with HTTP and gRPC interfaces.
One Go binary uses ONNX Runtime to classify photos and anime, detect explicit regions, and combine the results.
Optional Tesseract OCR detects words in images.

## Run with Docker

Run the CPU image:

```sh
docker run -e APP_ENV=web -p 4000:4000 m1chl/nsfw-sherlock
```

Make sure that the service responds:

```sh
curl http://localhost:4000/ping
```

The image includes the models and OCR support. It supports Linux amd64 and arm64.
Docker Hub and [GHCR](https://github.com/M1chlCZ/nsfw_sherlock/pkgs/container/nsfw_sherlock) publish the same images.

For gRPC, set `APP_ENV=grpc`. The default interface is HTTP.

## API

All image endpoints accept this JSON request:

```json
{"base64":"<base64-encoded image>","filename":"image.jpg"}
```

| HTTP endpoint | Response |
| --- | --- |
| `GET /ping` | Service status |
| `POST /pic/check` | `nsfwPic` and `nsfwText` flags |
| `POST /pic/labels` | Probabilities for `drawings`, `hentai`, `neutral`, `porn`, and `sexy` |
| `POST /pic/analyze` | Full analysis, model scores, region detections, and elapsed time |

Example response from `/pic/check`:

```json
{"status":"ok","message":"success","nsfwText":false,"nsfwPic":false}
```

The full analysis includes a `verdict` (`sfw`, `suggestive`, or `explicit`) and an `nsfw` score.
Errors use `{"errorMessage":"...","status":"FAIL","hasError":true}`.

The gRPC service exposes `Detect`, `DetectLabels`, and `Analyze`.
The [service definition](proto/nsfw.proto) contains the request and response types.

## GPU support

### NVIDIA · CUDA

Install an NVIDIA driver compatible with CUDA 12.9 and the NVIDIA Container Toolkit.
Then run the CUDA image:

```sh
docker run --gpus all -p 4000:4000 m1chl/nsfw-sherlock:v1.0.3-cuda
```

This image supports Linux amd64. It includes ONNX Runtime 1.29.0, CUDA 12, and cuDNN.
`ORT_DEVICE_ID` selects the GPU index, with `0` as the default.

### AMD · ROCm / MIGraphX

Install ROCm and a MIGraphX-enabled ONNX Runtime 1.29.0 shared library.
Set `ORT_PROVIDER=rocm`.
Point `ORT_LIB` to that library.
Keep the provider libraries beside it.
Add the ROCm dependencies to the library search path.

The `rocm` value is an alias for `migraphx`. The Go binding requires ORT API 29.
Older runtimes are incompatible. This project does not publish an AMD image.
The [MIGraphX guide](https://onnxruntime.ai/docs/build/eps.html#migraphx) describes the runtime build.

If the selected provider is unavailable, the service stops at startup.
Unsupported operators can still use the CPU.
**GPU inference remains untested on physical NVIDIA or AMD hardware.**

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_ENV` | `web` | HTTP (`web`) or gRPC (`grpc`). `http` is a deprecated alias. |
| `PORT` | `4000` | Service port |
| `NSFW_PROFILE` | `balanced` | Model profile |
| `ORT_PROVIDER` | `cpu` | `cpu`, `cuda`, or `migraphx`. `rocm` aliases `migraphx`. |
| `ORT_DEVICE_ID` | `0` | GPU index |
| `OCR_ENABLED` | `true` | Text detection. Requires a build with OCR support. |

<details>
<summary>Paths, limits, and logging</summary>

| Variable | Default | Purpose |
| --- | --- | --- |
| `MODELS_DIR` | `./assets/models` | Model directory |
| `MANIFEST` | `models/manifest.json` | Model manifest |
| `ORT_LIB` | Auto-detected | ONNX Runtime shared library path |
| `BAD_WORDS_FILE` | Auto-detected | OCR word list. Uses an embedded list as a fallback. |
| `MAX_IMAGE_BYTES` | `20971520` (20 MiB) | Maximum image size |
| `MAX_IMAGE_PIXELS` | `50000000` | Maximum decoded pixel count |
| `SESSION_POOL_SIZE` | `2` | Sessions per model |
| `NSFW_ALLOW_DEGRADED` | `false` | Allow startup after a required model fails to load. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `LOG_FORMAT` | `text` | `text` or `json` |

Docker images supply the model and runtime paths. They use JSON logs.

</details>

### Model profiles

| Profile | Photo model | Anime model | Region detector |
| --- | --- | --- | --- |
| `balanced` (default) | Freepik EVA02 | CaFormer | NudeNet 320px |
| `max` | Freepik EVA02 | CaFormer | NudeNet 640px |
| `fast` | Freepik EVA02 | MobileNetV3 | NudeNet 320px |
| `compat` | ViT5 with five legacy labels | None | None |

## Build from source

Requirements: Go 1.27, a C compiler, Python 3, and curl.
The runtime download supports Linux amd64, Linux arm64, and macOS arm64.

Download the source, runtime, and models.
Then build the binary.
Run the binary:

```sh
git clone https://github.com/M1chlCZ/nsfw_sherlock
cd nsfw_sherlock
scripts/fetch-models.sh --ort-only
scripts/fetch-models.sh
go build .
./nsfw_sherlock
```

The download script uses pinned SHA-256 digests for the runtime and models.
The engine also compares each model digest with the manifest at startup.
This source build excludes OCR and always reports `nsfwText: false`.

For OCR support, install Tesseract, Leptonica, their development libraries, and the English language data.
Then build with the OCR tag:

```sh
go build -tags ocr .
```

`OCR_ENABLED=false` also disables text detection.

## Development

Run the tests and lint checks:

```sh
go test ./... -count=1 -race
go test -tags ocr ./... -count=1 -race
golangci-lint run
golangci-lint run --build-tags ocr
PARITY_REQUIRE=1 go test ./engine -run TestParity
```

The OCR tests require Tesseract. The parity tests require the models and ONNX Runtime.
CI also runs `go vet`, `govulncheck`, and a Docker smoke test.
Release jobs require successful CI checks before they publish images.
The [lint configuration](.golangci.yml) adapts the [Golden config](https://github.com/maratori/golangci-lint-config).

Model tools: [manifest](models/manifest.json) · [download](scripts/fetch-models.sh) · [export](scripts/export-models.py) · [parity fixtures](scripts/inspect-models.py) · [benchmark](cmd/bench/main.go).
Stub generation uses `make proto`.

## Models and licenses

The code uses AGPL-3.0. Model licenses apply separately.

| Model | License |
| --- | --- |
| [Freepik photo classifier](https://huggingface.co/Freepik/nsfw_image_detector) | MIT |
| [deepghs anime classifiers](https://huggingface.co/deepghs/anime_rating) | MIT |
| [NudeNet region detectors](https://github.com/notAI-tech/NudeNet) | AGPL-3.0 |
| [ViT5 compatibility classifier](https://huggingface.co/giacomoarienti/nsfw-classifier) | CC-BY-NC-ND-4.0 |

Review the model licenses before commercial use.
The `compat` model has non-commercial and no-derivatives restrictions.
Review its license before you export or redistribute it.

Runtime: [onnxruntime_go](https://github.com/yalue/onnxruntime_go).
OCR: [gosseract](https://github.com/otiai10/gosseract) and Tesseract.
