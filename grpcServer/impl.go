package grpcserver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"nsfw_sherlock/engine"
	"nsfw_sherlock/grpcModels"
	"nsfw_sherlock/internal/ocrguard"
	"nsfw_sherlock/utils"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ocrConcurrency = 2
	ocrTimeout     = 10 * time.Second
)

// Analyzer is the engine surface the gRPC API needs.
type Analyzer interface {
	Analyze(ctx context.Context, data []byte) (engine.Analysis, error)
}

// TextChecker reports whether an encoded image contains bad words.
type TextChecker func(ctx context.Context, img []byte) (bool, error)

// Server implements the NSFW gRPC service.
type Server struct {
	grpcModels.UnimplementedNSFWServer

	analyzer Analyzer
	checker  TextChecker
	log      *slog.Logger
}

// NewServer builds the NSFW service backed by analyzer and checker.
func NewServer(analyzer Analyzer, checker TextChecker, log *slog.Logger) *Server {
	if analyzer == nil {
		panic("grpcServer: NewServer: nil analyzer")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		analyzer: analyzer,
		checker:  ocrguard.Guard(checker, ocrConcurrency, ocrTimeout),
		log:      log,
	}
}

// Detect reports whether an image or its embedded text is NSFW.
func (s *Server) Detect(ctx context.Context, req *grpcModels.NSFWRequest) (*grpcModels.NSFWResponse, error) {
	analysis, data, err := s.analyze(ctx, req.GetBase64())
	if err != nil {
		return nil, err
	}
	nsfwText, err := s.textNSFW(ctx, data)
	if err != nil {
		return nil, rpcError(err)
	}
	return &grpcModels.NSFWResponse{
		NsfwPicture: analysis.LegacyNSFW(),
		NsfwText:    nsfwText,
	}, nil
}

// DetectLabels returns the legacy five-label scores and text flag.
func (s *Server) DetectLabels(ctx context.Context, req *grpcModels.NSFWLabelsRequest) (*grpcModels.NSFWLabels, error) {
	analysis, data, err := s.analyze(ctx, req.GetBase64())
	if err != nil {
		return nil, err
	}
	nsfwText, err := s.textNSFW(ctx, data)
	if err != nil {
		return nil, rpcError(err)
	}
	return &grpcModels.NSFWLabels{
		Drawings: analysis.Labels.Drawings,
		Hentai:   analysis.Labels.Hentai,
		Neutral:  analysis.Labels.Neutral,
		Porn:     analysis.Labels.Porn,
		Sexy:     analysis.Labels.Sexy,
		NsfwText: nsfwText,
	}, nil
}

// Analyze returns the full engine analysis for one image plus the text flag.
func (s *Server) Analyze(ctx context.Context, req *grpcModels.NSFWRequest) (*grpcModels.Analysis, error) {
	analysis, data, err := s.analyze(ctx, req.GetBase64())
	if err != nil {
		return nil, err
	}
	nsfwText, err := s.textNSFW(ctx, data)
	if err != nil {
		return nil, rpcError(err)
	}
	return &grpcModels.Analysis{
		Verdict: analysis.Verdict,
		Nsfw:    analysis.NSFW,
		Labels: &grpcModels.NSFWLabels{
			Drawings: analysis.Labels.Drawings,
			Hentai:   analysis.Labels.Hentai,
			Neutral:  analysis.Labels.Neutral,
			Porn:     analysis.Labels.Porn,
			Sexy:     analysis.Labels.Sexy,
			NsfwText: nsfwText,
		},
		NsfwText:   nsfwText,
		Photo:      scoreMap(analysis.Photo),
		Anime:      scoreMap(analysis.Anime),
		Detections: detectionMessages(analysis.Detections),
		Models:     modelMessages(analysis.Models),
		ElapsedMs:  analysis.ElapsedMs,
	}, nil
}

func (s *Server) analyze(ctx context.Context, payload string) (engine.Analysis, []byte, error) {
	if payload == "" {
		return engine.Analysis{}, nil, status.Error(codes.InvalidArgument, "empty base64")
	}
	data, err := utils.DecodePayload(payload)
	if err != nil {
		return engine.Analysis{}, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	analysis, err := s.analyzer.Analyze(ctx, data)
	if err != nil {
		s.log.WarnContext(ctx, "grpc: analyze failed", "error", err)
		return engine.Analysis{}, nil, rpcError(err)
	}
	return analysis, data, nil
}

func (s *Server) textNSFW(ctx context.Context, data []byte) (bool, error) {
	if s.checker == nil {
		return false, nil
	}
	nsfw, err := s.checker(ctx, data)
	if err != nil {
		s.log.WarnContext(ctx, "grpc: text check failed", "error", err)
	}
	return nsfw, err
}

func rpcError(err error) error {
	switch {
	case errors.Is(err, engine.ErrImageTooLarge):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, engine.ErrUnsupportedImage):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func scoreMap(output *engine.ClassifierOutput) *grpcModels.ScoreMap {
	if output == nil {
		return nil
	}
	return &grpcModels.ScoreMap{Model: output.Model, Scores: output.Scores}
}

func detectionMessages(detections []engine.Detection) []*grpcModels.Detection {
	if len(detections) == 0 {
		return nil
	}
	out := make([]*grpcModels.Detection, 0, len(detections))
	for _, detection := range detections {
		out = append(out, &grpcModels.Detection{
			Label: detection.Label,
			Score: detection.Score,
			Box: &grpcModels.Box{
				X: detection.Box.X,
				Y: detection.Box.Y,
				W: detection.Box.W,
				H: detection.Box.H,
			},
		})
	}
	return out
}

func modelMessages(models []engine.ModelInfo) []*grpcModels.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*grpcModels.ModelInfo, 0, len(models))
	for _, model := range models {
		out = append(out, &grpcModels.ModelInfo{
			Id:     model.ID,
			Kind:   model.Kind,
			Sha256: model.SHA256,
		})
	}
	return out
}
