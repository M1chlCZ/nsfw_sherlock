# Use the official Golang image as the base image
FROM golang:1.20.3 as builder

# Install required dependencies
RUN apt-get update && apt-get install -y \
     wget \
     git \
     gcc \
     build-essential

# Download and install TensorFlow C library
RUN wget https://storage.googleapis.com/tensorflow/libtensorflow/libtensorflow-cpu-linux-x86_64-2.9.1.tar.gz && \
    tar -C /usr -xzf libtensorflow-cpu-linux-x86_64-2.9.1.tar.gz && \
    ldconfig && \
    rm libtensorflow-cpu-linux-x86_64-2.9.1.tar.gz

# Set the environment variables to help the Go compiler find the TensorFlow C library
ENV LD_LIBRARY_PATH /usr/local/lib
ENV CGO_CFLAGS "-I/usr/local/include"
ENV CGO_LDFLAGS "-L/usr/local/lib"

# Create a working directory for your Go project
WORKDIR /app

# Copy the Go project files into the container
COPY . .

# Build the Go project
RUN go mod tidy && \
    go build -o main .

#Use the official TensorFlow image as the base image
FROM ubuntu:22.04

COPY --from=builder /app/main .
COPY --from=builder /app/pic.jpg .
COPY --from=builder /app/assets/nsfw /assets/nsfw
COPY --from=builder /app/assets/temp /assets/temp

COPY --from=builder /usr/lib/libtensorflow.so.2 /usr/local/lib/
COPY --from=builder /usr/lib/libtensorflow_framework.so.2 /usr/local/lib/

# Set the environment variables to help the runtime find the TensorFlow C library
ENV LD_LIBRARY_PATH /usr/local/lib


LABEL authors="M1chl"

ENTRYPOINT ["./main"]
