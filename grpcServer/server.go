package grpcServer

import (
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"nsfw_sherlock/grpcModels"
	"nsfw_sherlock/utils"
)

func StartGrpcServer() {
	//test, err := common.TestPictureNSFW("./pic2.png")
	//if err != nil {
	//	utils.WrapErrorLog(err.Error())
	//	return
	//}
	//utils.ReportSuccess(fmt.Sprintf("NSFW PIC: %v", test))
	//
	//err = common.LoadBadWords()
	//if err != nil {
	//	utils.WrapErrorLog(err.Error())
	//	return
	//}
	//isSafeText, err := common.DetectTextNSFW("./pic2.png")
	//if err != nil {
	//	utils.WrapErrorLog(err.Error())
	//	return
	//}
	//utils.ReportSuccess(fmt.Sprintf("NSFW TEXT: %v", isSafeText))

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
