GO ?= go

.PHONY: proto

proto:
	mkdir -p grpcModels
	protoc -I proto --go_out=grpcModels --go_opt=paths=source_relative --go-grpc_out=grpcModels --go-grpc_opt=paths=source_relative proto/nsfw.proto
