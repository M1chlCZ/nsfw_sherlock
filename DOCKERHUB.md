# NSFW Sherlock

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://www.gnu.org/licenses/agpl-3.0.html)
[![Docker Hub](https://img.shields.io/docker/v/m1chl/nsfw-sherlock/latest?color=green&label=Docker%20Hub&style=flat)](https://hub.docker.com/repository/docker/m1chl/nsfw-sherlock/general)

An image classification API for NSFW content, with HTTP and gRPC interfaces.

One Go binary uses ONNX Runtime to classify photos and anime, detect explicit regions, and combine the results. Optional Tesseract OCR detects words in images.

The image includes the models and OCR support, and supports Linux amd64 and arm64.

## Quick start

Pull and run the CPU image:

```sh
docker pull m1chl/nsfw-sherlock
docker run -e APP_ENV=web -p 4000:4000 m1chl/nsfw-sherlock
```

Check that the service responds:

```sh
curl http://localhost:4000/ping
```

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

Example response from `/pic/analyze`:

```json
{
  "status": "ok",
  "message": "success",
  "verdict": "suggestive",
  "nsfw": 0.94,
  "labels": {"drawings": 0.01, "hentai": 0.02, "neutral": 0.03, "porn": 0.11, "sexy": 0.94},
  "photo": {"model": "photo-freepik", "scores": {"neutral": 0.03, "low": 0.02, "medium": 0.84, "high": 0.11}},
  "anime": {"model": "anime-caformer", "scores": {"safe": 0.97, "r15": 0.02, "r18": 0.01}},
  "detections": [{"label": "FEMALE_BREAST_COVERED", "score": 0.82, "box": {"x": 250, "y": 472, "w": 182, "h": 183}}],
  "nsfwText": false,
  "elapsedMs": 187
}
```

The full analysis includes a `verdict` (`sfw`, `suggestive`, or `explicit`) and an `nsfw` score.

Errors use this shape:

```json
{"errorMessage":"...","status":"FAIL","hasError":true}
```

The gRPC service exposes `Detect`, `DetectLabels`, and `Analyze`.
The [service definition](https://github.com/M1chlCZ/nsfw_sherlock/blob/main/proto/nsfw.proto) contains the request and response types.

## GPU support

### NVIDIA · CUDA

Install an NVIDIA driver compatible with CUDA 12.9 and the NVIDIA Container Toolkit.
CUDA images are tagged with a `-cuda` suffix, for example `v1.0.4-cuda` and `latest-cuda`.

```sh
docker run --gpus all -p 4000:4000 m1chl/nsfw-sherlock:latest-cuda
```

This image supports Linux amd64. It includes ONNX Runtime 1.29.0, CUDA 12, and cuDNN.
`ORT_DEVICE_ID` selects the GPU index, with `0` as the default.

### AMD · ROCm / MIGraphX

Install ROCm and a MIGraphX-enabled ONNX Runtime 1.29.0 shared library.
Set `ORT_PROVIDER=rocm` and point `ORT_LIB` to that library.
Keep the provider libraries beside it and add the ROCm dependencies to the library search path.

The `rocm` value is an alias for `migraphx`. The Go binding requires ORT API 29, so older runtimes are incompatible. This project does not publish an AMD image.

If the selected provider is unavailable, the service stops at startup. Unsupported operators can still use the CPU.

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

Docker images supply the model and runtime paths and use JSON logs.

### Model profiles

| Profile | Photo model | Anime model | Region detector |
| --- | --- | --- | --- |
| `balanced` (default) | Freepik EVA02 | CaFormer | NudeNet 320px |
| `max` | Freepik EVA02 | CaFormer | NudeNet 640px |
| `fast` | Freepik EVA02 | MobileNetV3 | NudeNet 320px |
| `compat` | ViT5 with five legacy labels | None | None |

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

Runtime: [onnxruntime_go](https://github.com/yalue/onnxruntime_go).
OCR: [gosseract](https://github.com/otiai10/gosseract) and Tesseract.

Source, issues, and documentation: [github.com/M1chlCZ/nsfw_sherlock](https://github.com/M1chlCZ/nsfw_sherlock).
