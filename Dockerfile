ARG RUNTIME_IMAGE=debian:bookworm-slim
FROM --platform=$BUILDPLATFORM python:3.15-slim AS models

ARG MODELS_BASE_URL=""

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY scripts/ scripts/
COPY models/manifest.json /models/manifest.json

RUN if [ -n "$MODELS_BASE_URL" ]; then \
      curl -fsSL -o /models/freepik-eva02-448.onnx "$MODELS_BASE_URL/freepik-eva02-448.onnx" \
      && curl -fsSL -o /models/nsfw-vit5-224.onnx "$MODELS_BASE_URL/nsfw-vit5-224.onnx"; \
    else \
      pip install --no-cache-dir -r scripts/requirements-export.txt \
      && python scripts/export-models.py --out /models; \
    fi \
    && bash scripts/fetch-models.sh --dir /models --manifest /models/manifest.json

FROM golang:1.27.4-bookworm AS builder

ARG VERSION=dev

RUN apt-get update \
    && apt-get install -y --no-install-recommends libtesseract-dev libleptonica-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags ocr -ldflags "-s -w -X main.version=${VERSION}" -o /out/nsfw-sherlock .

FROM ${RUNTIME_IMAGE} AS runtime

ARG TARGETARCH
ARG ORT_VARIANT=cpu
ARG ORT_PROVIDER=cpu
ARG ORT_VERSION=1.31.1
ARG ORT_WHEEL_AMD64=https://files.pythonhosted.org/packages/eb/27/c6a823bac55b2977bea2027c3ec9393a2c1002cb9a09eb40354563e02730/onnxruntime-1.31.1-cp312-cp312-manylinux_2_28_x86_64.whl
ARG ORT_WHEEL_ARM64=https://files.pythonhosted.org/packages/ba/e4/1ed1a7a5f4702d899091fa2b976d377995250c307326ec8dc9ae6378aa13/onnxruntime-1.31.1-cp312-cp312-manylinux_2_28_aarch64.whl
ARG ORT_WHEEL_CUDA13=https://files.pythonhosted.org/packages/22/97/9531b4d9e566cf2a0f04b4b5c26a3bc0ef2e2e0b6bc943a02c0eead98cf7/onnxruntime_gpu-1.31.1-cp312-cp312-manylinux_2_28_x86_64.whl
ARG ORT_SHA256_AMD64=1f3dea47f2f0e5117c2404cea7d21fd253ae7836a3bf167a501cdfbb2600d536
ARG ORT_SHA256_ARM64=0b9a492176886992e0e875f06bc0367e333f5fff11711a2d642e18b02f3009cc
ARG ORT_SHA256_CUDA13=05d548956197b9d01f59b625de740e0d6be3593f874e241a12dc0362650ecdf7

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl ffmpeg libtesseract5 liblept5 tesseract-ocr tesseract-ocr-eng unzip \
    && rm -rf /var/lib/apt/lists/*

RUN arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$arch/$ORT_VARIANT" in \
      amd64/cpu) ort_url="$ORT_WHEEL_AMD64"; ort_sha="$ORT_SHA256_AMD64" ;; \
      arm64/cpu) ort_url="$ORT_WHEEL_ARM64"; ort_sha="$ORT_SHA256_ARM64" ;; \
      amd64/cuda13) ort_url="$ORT_WHEEL_CUDA13"; ort_sha="$ORT_SHA256_CUDA13" ;; \
      *) echo "unsupported architecture/runtime: $arch/$ORT_VARIANT" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/ort.whl "$ort_url" \
    && printf '%s  %s\n' "$ort_sha" /tmp/ort.whl | sha256sum -c - \
    && mkdir -p /app/lib \
    && unzip -j -o /tmp/ort.whl 'onnxruntime/capi/libonnxruntime*.so*' -d /app/lib \
    && ln -sf "libonnxruntime.so.${ORT_VERSION}" /app/lib/libonnxruntime.so \
    && rm -f /tmp/ort.whl

RUN useradd -r -u 10001 appuser

COPY --from=builder /out/nsfw-sherlock /app/nsfw-sherlock
COPY --from=models /models /app/models

RUN chown -R appuser:appuser /app

WORKDIR /app
ENV APP_ENV=web \
    ORT_PROVIDER=${ORT_PROVIDER} \
    LOG_FORMAT=json \
    MODELS_DIR=/app/models \
    ORT_LIB=/app/lib/libonnxruntime.so \
    TESSDATA_PREFIX=/usr/share/tesseract-ocr/5/tessdata

USER appuser
EXPOSE 4000
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD sh -c 'if [ "$APP_ENV" = "grpc" ]; then exit 0; fi; curl -fsS "http://localhost:${PORT:-4000}/ping" || exit 1'
ENTRYPOINT ["/app/nsfw-sherlock"]
