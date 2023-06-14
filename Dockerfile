# Use the official Golang image as the base image
FROM golang:1.20.3 as builder

# Install required dependencies
RUN apt-get update && apt-get install -y \
     wget \
     git \
     gcc \
    unzip \
     build-essential

RUN apt-get install -y -qq libtesseract-dev libleptonica-dev

# Install protobuf compiler and gRPC plugins
RUN apt-get install -y protobuf-compiler
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.28
RUN go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.2
RUN mv /go/bin/protoc-gen-go* /usr/local/bin/

# Download and install TensorFlow C library
RUN wget https://storage.googleapis.com/tensorflow/libtensorflow/libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz && \
    tar -C /usr -xzf libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz && \
    ldconfig && \
    rm libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz

# Set the environment variables to help the Go compiler find the TensorFlow C library
ENV LD_LIBRARY_PATH /usr/local/lib
ENV CGO_CFLAGS "-I/usr/local/include"
ENV CGO_LDFLAGS "-L/usr/local/lib"

# Create a working directory for your Go project
WORKDIR /app

# Copy the Go project files into the container
COPY . .

# Create the required directories
RUN mkdir -p grpcModels
RUN mkdir -p assets/temp
RUN mkdir -p assets/nsfw

RUN wget -q https://github.com/GantMan/nsfw_model/releases/download/1.1.0/nsfw_mobilenet_v2_140_224.zip

RUN unzip nsfw_mobilenet_v2_140_224.zip && mv mobilenet_v2_140_224/* /app/assets/nsfw/ && rm -r mobilenet_v2_140_224 nsfw_mobilenet_v2_140_224.zip

# Compile the .proto files
RUN cd ./proto && \
    protoc --go_out=../grpcModels --go_opt=paths=source_relative --go-grpc_out=../grpcModels --go-grpc_opt=paths=source_relative *.proto

# Build the Go project
RUN go mod tidy && \
    go build -o main .

#Use the official TensorFlow image as the base image
FROM debian:stable-slim

# Install required dependencies
RUN apt-get update -qq
RUN apt-get install -y -qq libtesseract-dev libleptonica-dev
ENV TESSDATA_PREFIX=/usr/share/tesseract-ocr/4.00/tessdata/
RUN apt-get install -y -qq tesseract-ocr-eng

COPY --from=builder /app/main .
COPY --from=builder /app/pic.jpg .
COPY --from=builder /app/bad_words_fallback.txt .
COPY --from=builder /app/assets/nsfw /assets/nsfw
COPY --from=builder /app/assets/temp /assets/temp
COPY --from=builder /app/labels.txt /assets/nsfw/labels.txt

RUN wget https://storage.googleapis.com/tensorflow/libtensorflow/libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz && \
    tar -C /usr -xzf libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz && \
    ldconfig && \
    rm libtensorflow-cpu-linux-x86_64-2.11.0.tar.gz

# Set the environment variables to help the runtime find the TensorFlow C library
ENV LD_LIBRARY_PATH /usr/local/lib
RUN ldconfig

LABEL authors="M1chl"

CMD ["./main"]
