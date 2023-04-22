package grpcServer

import (
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"nsfw_sherlock/common"
	"nsfw_sherlock/grpcModels"
	"nsfw_sherlock/utils"
)

func StartGrpcServer() {
	// Dry run for TF
	test, err := common.TestPictureNSFW("./pic.jpg")
	if err != nil {
		utils.WrapErrorLog(err.Error())
		return
	}
	utils.ReportSuccess(fmt.Sprintf("NSFW PIC: %v", test))

	//Load bad words
	err = common.LoadBadWords()
	if err != nil {
		utils.WrapErrorLog("Can't load bad words")
		return
	}

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
