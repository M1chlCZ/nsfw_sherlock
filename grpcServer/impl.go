package grpcServer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"nsfw_sherlock/common"
	"nsfw_sherlock/grpcModels"
	"nsfw_sherlock/utils"
	"os"
	"strings"
)

type Server struct {
	grpcModels.UnimplementedNSFWServer
}

func (s *Server) Detect(_ context.Context, req *grpcModels.NSFWRequest) (*grpcModels.NSFWResponse, error) {
	if req.Base64 == "" {
		return &grpcModels.NSFWResponse{}, errors.New("empty base64")
	}
	decoded, err := utils.DecodePayload([]byte(req.Base64))
	if err != nil {
		return &grpcModels.NSFWResponse{}, err
	}
	suffix := strings.Split(req.Filename, ".")[1]
	if _, err := os.Stat("/assets/temp"); os.IsNotExist(err) {
		err := os.Mkdir("/assets/temp", os.ModePerm)
		if err != nil {
			return &grpcModels.NSFWResponse{}, err
		}
	}
	filename := fmt.Sprintf("./assets/temp/%s.%s", utils.GenerateSecureToken(8), suffix)
	err = os.WriteFile(filename, decoded, 0644)
	if err != nil {
		return &grpcModels.NSFWResponse{}, err
	}
	defer func() {
		err := os.Remove(filename)
		if err != nil {
			log.Println(err.Error())
		}
	}()
	isNSFW, err := common.TestPictureNSFW(filename)
	if err != nil {
		return &grpcModels.NSFWResponse{}, err
	} else {
		utils.ReportSuccess(fmt.Sprintf("NSFW PIC: %v", isNSFW))
	}
	isNSFWText, err := common.DetectTextNSFW(filename)
	if err != nil {
		return &grpcModels.NSFWResponse{}, err
	}
	utils.ReportMessage(fmt.Sprintf("NSFW TEXT: %v", isNSFWText))

	return &grpcModels.NSFWResponse{
		NsfwPicture: isNSFW,
		NsfwText:    isNSFWText,
	}, nil
}

func (s *Server) DetectLabels(_ context.Context, req *grpcModels.NSFWLabelsRequest) (*grpcModels.NSFWLabels, error) {
	if req.Base64 == "" {
		return &grpcModels.NSFWLabels{}, errors.New("empty base64")
	}
	decoded, err := utils.DecodePayload([]byte(req.Base64))
	if err != nil {
		return &grpcModels.NSFWLabels{}, err
	}
	suffix := strings.Split(req.Filename, ".")[1]
	if _, err := os.Stat("/assets/temp"); os.IsNotExist(err) {
		err := os.Mkdir("/assets/temp", os.ModePerm)
		if err != nil {
			return &grpcModels.NSFWLabels{}, err
		}
	}
	filename := fmt.Sprintf("./assets/temp/%s.%s", utils.GenerateSecureToken(8), suffix)
	err = os.WriteFile(filename, decoded, 0644)
	if err != nil {
		return &grpcModels.NSFWLabels{}, err
	}
	defer func() {
		err := os.Remove(filename)
		if err != nil {
			log.Println(err.Error())
		}
	}()
	labels, err := common.TestPictureNSFWLabels(filename)
	if err != nil {
		return &grpcModels.NSFWLabels{}, err
	} else {
		utils.ReportSuccess(fmt.Sprintf("RESULT LABELS: %v", labels.GetLabels()))
	}
	isNSFWText, err := common.DetectTextNSFW(filename)
	if err != nil {
		return &grpcModels.NSFWLabels{}, err
	}
	utils.ReportMessage(fmt.Sprintf("NSFW TEXT: %v", isNSFWText))

	return &grpcModels.NSFWLabels{
		Drawings: labels.GetLabels().Drawings,
		Hentai:   labels.GetLabels().Hentai,
		Neutral:  labels.GetLabels().Neutral,
		Porn:     labels.GetLabels().Porn,
		Sexy:     labels.GetLabels().Sexy,
		NsfwText: isNSFWText,
	}, nil
}
