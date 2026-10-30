ARG RUNTIME_IMAGE=debian:bookworm-slim
FROM --platform=$BUILDPLATFORM python:3.12-slim AS models

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

FROM golang:1.27.3-bookworm AS builder

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
ARG ORT_SHA256_AMD64=c849e36a1ef3e22d4ff217b9d1ce4a3cf5b9467b975610913ae959ad5b16bdc9
ARG ORT_SHA256_ARM64=5317505e2da99be65f8e2775ba3c2afd6d2961fd78aba1bd8f56159f48820c78
ARG ORT_SHA256_CUDA12=57cd51f677eca19a269cdba31b0c16c15d73a8feea41f170aa8b750447ab1f70

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl ffmpeg libtesseract5 liblept5 tesseract-ocr tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*

RUN arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$arch/$ORT_VARIANT" in \
      amd64/cpu) ort_arch=x64; ort_sha="$ORT_SHA256_AMD64" ;; \
      arm64/cpu) ort_arch=aarch64; ort_sha="$ORT_SHA256_ARM64" ;; \
      amd64/cuda12) ort_arch=x64-gpu_cuda12; ort_sha="$ORT_SHA256_CUDA12" ;; \
      *) echo "unsupported architecture/runtime: $arch/$ORT_VARIANT" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/ort.tgz "https://github.com/microsoft/onnxruntime/releases/download/v1.31.1/onnxruntime-linux-${ort_arch}-1.31.1.tgz" \
    && printf '%s  %s\n' "$ort_sha" /tmp/ort.tgz | sha256sum -c - \
    && tar -xzf /tmp/ort.tgz -C /tmp \
    && mkdir -p /app/lib \
    && cp -P "/tmp/onnxruntime-linux-${ort_arch}-1.31.1/lib/"*.so* /app/lib/ \
    && rm -rf /tmp/ort.tgz "/tmp/onnxruntime-linux-${ort_arch}-1.31.1"

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
