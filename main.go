package main

import (
	"nsfw_sherlock/grpcServer"
	"nsfw_sherlock/utils"
	"nsfw_sherlock/web"
	"os"
)

func main() {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "web"
	}

	if appEnv == "web" {
		utils.ReportMessage("Starting web server...")
		web.StartWebServer()
	} else if appEnv == "grpc" {
		utils.ReportMessage("Starting grpc server...")
		grpcServer.StartGrpcServer()
	}
}
