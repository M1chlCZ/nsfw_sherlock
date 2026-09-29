package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"runtime/debug"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/grpcModels"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	maxConcurrentStreams = 64
	bodyLimitOverhead    = 64 << 10
	bodyLimitFloor       = 1 << 20
)

var (
	gracefulTimeout = 10 * time.Second
	rpcTimeout      = 30 * time.Second
	rpcConcurrency  = 16
)

// StartGrpcServer serves the NSFW API on :cfg.Port until ctx is cancelled.
func StartGrpcServer(
	ctx context.Context,
	cfg config.Config,
	analyzer Analyzer,
	checker TextChecker,
	log *slog.Logger,
) error {
	if log == nil {
		log = slog.Default()
	}
	timeout := gracefulTimeout

	addr := fmt.Sprintf(":%d", cfg.Port)
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("grpcServer: cannot listen on %s: %w", addr, err)
	}
	defer func() { _ = lis.Close() }()

	limit := messageLimitFor(cfg.MaxImageBytes)
	server := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.MaxRecvMsgSize(limit),
		grpc.MaxSendMsgSize(limit),
		grpc.MaxConcurrentStreams(maxConcurrentStreams),
		grpc.ChainUnaryInterceptor(unaryInterceptors(log)...),
	)
	grpcModels.RegisterNSFWServer(server, NewServer(analyzer, checker, log))

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	select {
	case err := <-serveErr:
		return serveError(err)
	case <-ctx.Done():
	}

	log.InfoContext(ctx, "grpc: shutting down server", "timeout", timeout)
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()
	stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-stopped:
	case <-stopCtx.Done():
		log.WarnContext(ctx, "grpc: graceful stop timed out, forcing stop")
		go server.Stop()
		return fmt.Errorf("grpcServer: graceful stop timed out after %s: %w", timeout, stopCtx.Err())
	}
	return serveError(<-serveErr)
}

func serveError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("grpcServer: serve: %w", err)
}

func messageLimitFor(maxImageBytes int64) int {
	if maxImageBytes <= 0 {
		return bodyLimitFloor
	}
	const maxInt32 = int64(math.MaxInt32)
	if maxImageBytes > maxInt32/4*3 {
		return math.MaxInt32
	}
	limit := maxImageBytes/3*4 + 4 + bodyLimitOverhead
	if limit < bodyLimitFloor {
		return bodyLimitFloor
	}
	if limit > maxInt32 {
		return math.MaxInt32
	}
	return int(limit)
}

func unaryInterceptors(log *slog.Logger) []grpc.UnaryServerInterceptor {
	return []grpc.UnaryServerInterceptor{
		recoveryInterceptor(log),
		timeoutInterceptor(),
		concurrencyInterceptor(),
	}
}

func recoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(
					ctx,
					"grpc: panic recovered",
					"method",
					info.FullMethod,
					"panic",
					r,
					"stack",
					string(debug.Stack()),
				)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

func timeoutInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if rpcTimeout <= 0 {
			return handler(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		defer cancel()
		resp, err := handler(ctx, req)
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return resp, err
	}
}

func concurrencyInterceptor() grpc.UnaryServerInterceptor {
	limit := max(rpcConcurrency, 1)
	slots := make(chan struct{}, limit)
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		select {
		case slots <- struct{}{}:
		default:
			return nil, status.Error(codes.ResourceExhausted, "server is busy")
		}
		defer func() { <-slots }()
		return handler(ctx, req)
	}
}
