<p align="center">
  <img src="docs/brand/logo.svg" alt="NSFW Sherlock logo: a magnifying glass over an image" width="128" height="128">
</p>

<h1 align="center">NSFW Sherlock</h1>

<p align="center"><strong>Free, self-hosted image moderation.</strong><br>Run it on your server. Keep image analysis local.</p>

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://www.gnu.org/licenses/agpl-3.0.html)
[![Docker Hub](https://img.shields.io/docker/v/m1chl/nsfw-sherlock/latest?color=green&label=Docker%20Hub&style=flat)](https://hub.docker.com/repository/docker/m1chl/nsfw-sherlock/general)

NSFW Sherlock is a free, open-source API that checks images for NSFW content on your own hardware.
It has no subscription or per-image API fees. You cover your own hardware and hosting costs.

- **Self-hosted:** Image analysis runs on your server, without an external moderation API.
- **Easy to connect:** Use HTTP or gRPC from your application.
- **Image checks:** Classify photos and anime, and detect explicit regions.
- **Optional OCR:** Use Tesseract to detect words in images.
- **Docker:** Start with one command on Linux amd64 or arm64.

One Go binary runs the models with ONNX Runtime.
The code uses AGPL-3.0. [Model licenses](#models-and-licenses) apply separately.

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

## Run a release binary

The [releases page](https://github.com/M1chlCZ/nsfw_sherlock/releases) has archives for Linux amd64, Linux arm64, and macOS arm64, with checksums.
The binaries do not include OCR support.

Clone the repository and unpack the archive into it.
Then download the models and the runtime:

```sh
scripts/fetch-models.sh --ort-only
scripts/fetch-models.sh
./nsfw-sherlock
```

## API

All image endpoints accept this JSON request:

```json
{"base64":"<base64-encoded image>","filename":"image.jpg"}
```

Set `sensitivity` between 0.1 and 10 to change the decision thresholds. Higher values flag more images. The default is 1. The gRPC requests have the same field.

With `API_KEYS`, every image endpoint requires the key in the `X-API-Key` header. `/ping` and `/metrics` stay open.

| HTTP endpoint | Response |
| --- | --- |
| `GET /ping` | Service status |
| `GET /metrics` | Prometheus metrics |
| `POST /pic/check` | `nsfwPic` and `nsfwText` flags |
| `POST /pic/labels` | Probabilities for `drawings`, `hentai`, `neutral`, `porn`, and `sexy` |
| `POST /pic/analyze` | Full analysis, model scores, region detections, and elapsed time |
| `POST /pic/batch` | One `/pic/check` result per image, up to `BATCH_MAX_IMAGES` |
| `POST /pic/upload` | Raw image bytes as the request body; the same body as `/pic/check` |
| `POST /video/check` | Multipart video upload; one analysis per sampled frame and a summary |
| `GET /ready` | Readiness probe; reports whether video scanning is available |
| `POST /jobs` | Start a background batch or video analysis |
| `GET /jobs` | Kept background jobs, newest first, with state counts |
| `GET /jobs/{id}` | Job state and result |
| `GET /jobs/{id}/events` | Server-sent events for a job's state changes |
| `DELETE /jobs/{id}` | Cancel a queued job or a running analysis |

Example response from `/pic/check`:

```json
{"status":"ok","message":"success","nsfwText":false,"nsfwPic":false}
```

The full analysis includes a `verdict` (`sfw`, `suggestive`, or `explicit`) and an `nsfw` score.
Errors use `{"errorMessage":"...","status":"FAIL","hasError":true}`.

The batch endpoint accepts `{"images":[<the request above>, ...]}` and returns `{"status":"ok","message":"success","results":[...]}`.
Each result is a `/pic/check` body or the error body.
The default limit is 4 images.

When `CACHE_ENTRIES` is greater than 0, the service stores responses for identical image bytes and returns them again without running the models.

### Video scanning

`POST /video/check` accepts a multipart form with the video in the `file`
field. The service samples frames with ffmpeg (`VIDEO_SAMPLE_FPS`, default 1
frame per second, at most `VIDEO_MAX_FRAMES`, default 24), analyzes each frame,
and returns the worst verdict:

```sh
curl -fsS -F "file=@clip.mp4" http://localhost:4000/video/check
```

The response holds `summary` (verdict, maximum NSFW score, frame counts and
elapsed time) and `frames`, with one analysis per sampled frame. Install
ffmpeg on the host, or use the Docker image, which includes it. Without ffmpeg
the endpoint answers 503. Uploads are limited to `VIDEO_MAX_BYTES` and the
whole scan stops after 120 seconds.

Every response carries an `X-Request-ID` header. JSON logs repeat it as `requestId`.

### Async jobs

Large batches and videos can run in the background. `POST /jobs` accepts the
same JSON body as `/pic/batch`, or a multipart video in the `file` field:

```sh
curl -fsS -X POST http://localhost:4000/jobs \
  -H "Content-Type: application/json" -d '{"images":[...]}'
curl -fsS -F "file=@clip.mp4" http://localhost:4000/jobs
```

The service answers 202 with a job id. Poll `GET /jobs/{id}` for the state
(`queued`, `running`, `done`, `failed` or `canceled`) and the result.
`DELETE /jobs/{id}` cancels a queued job or a running analysis.

`GET /jobs` lists the kept jobs newest first. Pass `state` to filter
(`queued`, `running`, `done`, `failed` or `canceled`) and `limit` to cap the
result; the default is 100. The `counts` field always covers every kept job.

`GET /jobs/{id}/events` streams the state changes with server-sent events:

```sh
curl -N http://localhost:4000/jobs/<id>/events
```

Each event is named after the state and carries the same body as
`GET /jobs/{id}`. The stream ends when the job finishes. A keepalive comment
every 15 seconds keeps the connection open.

`JOB_WORKERS` sets how many jobs run at once, `JOB_MAX_ENTRIES` caps the kept
jobs, and `JOB_TTL_SECONDS` sets how long a finished job stays. The defaults
are 2 workers, 64 jobs, and 600 seconds.

The `nsfw_request_duration_seconds` histogram carries an `endpoint` label.
The `/metrics` output also includes `nsfw_jobs_submitted_total`,
`nsfw_jobs_rejected_total`, `nsfw_jobs_current`, `nsfw_jobs_finished_total`,
and `nsfw_job_duration_seconds`. `GET /ready` reports whether video scanning is
available.

The gRPC service exposes `Detect`, `DetectLabels`, and `Analyze`.
It also serves the standard gRPC health service and server reflection.
The [service definition](proto/nsfw.proto) contains the request and response types.

## GPU support

### NVIDIA · CUDA

Install an NVIDIA driver compatible with CUDA 12.9 and the NVIDIA Container Toolkit.
Then run the CUDA image:

```sh
docker run --gpus all -p 4000:4000 m1chl/nsfw-sherlock:v1.3.1-cuda
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

### NVIDIA · TensorRT

Use an ONNX Runtime shared library built with the TensorRT execution provider.
Then set `ORT_PROVIDER=tensorrt`.
TensorRT compiles each model for your GPU at the first run. This step is slow.

### Intel · OpenVINO

Use an ONNX Runtime shared library built with the OpenVINO execution provider.
Then set `ORT_PROVIDER=openvino`.
OpenVINO runs on an Intel CPU or GPU.

If the selected provider is unavailable, the service stops at startup.
Unsupported operators can still use the CPU.
The v1.0.5 Linux amd64 binary passed a physical CUDA test on an RTX 5070 Ti under WSL2, with ONNX Runtime 1.29.0, CUDA 13 and cuDNN 9.24. See the [test setup, results and reproduction client](https://gist.github.com/M1chlCZ/35c5a7b94e729b8a6e9403b739981c79). This test does not cover the CUDA 12 Docker image, AMD/MIGraphX, TensorRT or OpenVINO.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_ENV` | `web` | HTTP (`web`) or gRPC (`grpc`). `http` is a deprecated alias. |
| `PORT` | `4000` | Service port |
| `NSFW_PROFILE` | `balanced` | Model profile |
| `ORT_PROVIDER` | `cpu` | `cpu`, `cuda`, `migraphx`, `tensorrt`, or `openvino`. `rocm` aliases `migraphx`. |
| `ORT_DEVICE_ID` | `0` | GPU index |
| `OCR_ENABLED` | `true` | Text detection. Requires a build with OCR support. |
| `API_KEYS` | (none) | Comma-separated keys for the `X-API-Key` header. Empty disables the check. |
| `RATE_LIMIT_RPM` | `0` | Requests per minute for each key or IP. `0` disables the limit. |

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
| `BATCH_MAX_IMAGES` | `4` | Images per `/pic/batch` request (1..64). Raises the request body limit. |
| `CACHE_ENTRIES` | `0` | Stored responses for identical image bytes. `0` disables the cache. |
| `JOB_WORKERS` | `2` | Background jobs that run at once (1..32) |
| `JOB_MAX_ENTRIES` | `64` | Kept background jobs (1..10000) |
| `JOB_TTL_SECONDS` | `600` | Seconds a finished job stays available (1..86400) |
| `FFMPEG_PATH` | `ffmpeg` | ffmpeg binary used by `/video/check` |
| `VIDEO_SAMPLE_FPS` | `1` | Frames sampled per second (0.1..30) |
| `VIDEO_MAX_FRAMES` | `24` | Frame cap per video (1..240) |
| `VIDEO_MAX_BYTES` | `67108864` (64 MiB) | Maximum video upload |
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

## Support

If this project helps you, you can support its development.

[![Support me on Ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/m1chl)
<a href="https://buymeacoffee.com/xdujifyzvt"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-yellow.png" alt="Buy me a coffee" height="30"></a>

If this project helps you, consider giving it a star. Bug reports and contributions are welcome.
