package grpcServer

import (
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log"
	"net"
	"nsfw_sherlock/grpcModels"
	"nsfw_sherlock/nsfw"
	"nsfw_sherlock/utils"
	"path/filepath"
)

var modelPath, _ = filepath.Abs("./assets/nsfw")
var detector = nsfw.New(modelPath)

func StartGrpcServer() {
	test, err := TestGrpc("./pic.jpg")
	if err != nil {
		utils.WrapErrorLog(err.Error())
		return
	}
	utils.ReportSuccess(fmt.Sprintf("NSFW: %v", !test))

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", 4000))
	if err != nil {
		utils.WrapErrorLog(err.Error())
		return
	}

	utils.ReportMessage(fmt.Sprintf("gRPC Online on port %d!", 4000))

	s := Server{}
	//, grpc.UnaryInterceptor(serverInterceptor)
	grpcServer := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	grpcModels.RegisterNSFWServer(grpcServer, &s)
	if err := grpcServer.Serve(lis); err != nil {
		utils.WrapErrorLog(err.Error())
	}
}

func TestGrpc(filename string) (bool, error) {
	l, err := detect(filename)
	if err != nil {
		return false, err
	}
	return l.IsSafe(), nil
}

func detect(filename string) (nsfw.Labels, error) {
	result, err := detector.File(filename)
	if err != nil {
		log.Fatalln(err.Error())
		return result, err
	}

	return result, nil
}
