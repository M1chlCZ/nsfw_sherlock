cd ./proto
protoc --go_out=../grpcModels --go_opt=paths=source_relative --go-grpc_out=../grpcModels --go-grpc_opt=paths=source_relative *.proto
cd ..
