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

FROM golang:1.27-bookworm AS builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends libtesseract-dev libleptonica-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags ocr -ldflags "-s -w" -o /out/nsfw-sherlock .

FROM ${RUNTIME_IMAGE} AS runtime

ARG TARGETARCH
ARG ORT_VARIANT=cpu
ARG ORT_PROVIDER=cpu
ARG ORT_SHA256_AMD64=c3fddc4f139a045b0c4902c57410f0694f1c2fdf9b6939fbe38b1aeae7cd14ba
ARG ORT_SHA256_ARM64=e1799098ebc054b370f6176a450f158720f297818c613e5dc99b92e2ec82346f
ARG ORT_SHA256_CUDA12=4ca594a0da83927befbd73fe020d7f569be151d70bb4fe9741ad405f4882e2ad

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl libtesseract5 liblept5 tesseract-ocr tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*

RUN arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$arch/$ORT_VARIANT" in \
      amd64/cpu) ort_arch=x64; ort_sha="$ORT_SHA256_AMD64" ;; \
      arm64/cpu) ort_arch=aarch64; ort_sha="$ORT_SHA256_ARM64" ;; \
      amd64/cuda12) ort_arch=x64-gpu_cuda12; ort_sha="$ORT_SHA256_CUDA12" ;; \
      *) echo "unsupported architecture/runtime: $arch/$ORT_VARIANT" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/ort.tgz "https://github.com/microsoft/onnxruntime/releases/download/v1.29.0/onnxruntime-linux-${ort_arch}-1.29.0.tgz" \
    && printf '%s  %s\n' "$ort_sha" /tmp/ort.tgz | sha256sum -c - \
    && tar -xzf /tmp/ort.tgz -C /tmp \
    && mkdir -p /app/lib \
    && cp -P "/tmp/onnxruntime-linux-${ort_arch}-1.29.0/lib/"*.so* /app/lib/ \
    && rm -rf /tmp/ort.tgz "/tmp/onnxruntime-linux-${ort_arch}-1.29.0"

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
