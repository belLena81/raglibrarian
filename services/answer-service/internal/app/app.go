// Package app composes the Answer process and its infrastructure adapters.
package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	sharedlogger "github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/answer-service/config"
	"github.com/belLena81/raglibrarian/services/answer-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	answergrpc "github.com/belLena81/raglibrarian/services/answer-service/internal/grpc"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/metrics"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/retrieval"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type App struct {
	grpcServer   *grpc.Server
	httpServer   *http.Server
	grpcListener net.Listener
	httpListener net.Listener
	connection   *grpc.ClientConn
	service      *application.Service
	metrics      *metrics.Recorder
	log          *zap.Logger
}

func New(configuration config.Config) (*App, error) {
	log, err := sharedlogger.New("answer-service")
	if err != nil {
		return nil, errors.New("configure diagnostics")
	}
	serverCredentials, err := internaltls.ServerCredentials(configuration.TLS)
	if err != nil {
		return nil, errors.New("load server transport credentials")
	}
	clientCredentials, err := internaltls.ClientCredentials(configuration.TLS, configuration.RetrievalDNSName)
	if err != nil {
		return nil, errors.New("load client transport credentials")
	}
	apiKey, err := provider.ReadAPIKey(configuration.LLMAPIKeyFile)
	if err != nil {
		return nil, errors.New("load provider credentials")
	}
	httpClient, err := provider.NewHTTPClient(configuration.LLMCAFile)
	if err != nil {
		return nil, errors.New("configure provider transport")
	}
	providerAdapter, err := provider.NewOpenAI(configuration.LLMBaseURL, configuration.LLMModel, apiKey, httpClient)
	if err != nil {
		return nil, errors.New("configure provider")
	}
	if err = process.DropPrivileges(configuration.RunAs); err != nil {
		return nil, errors.New("reduce process privileges")
	}
	connection, err := grpc.NewClient(configuration.RetrievalAddress, grpc.WithTransportCredentials(clientCredentials))
	if err != nil {
		return nil, errors.New("configure retrieval client")
	}
	retriever := retrieval.NewClient(retrievalv1.NewRetrievalServiceClient(connection))
	metricRecorder := &metrics.Recorder{}
	service, err := application.NewService(retriever, providerAdapter, diagnostic.New(log, metricRecorder), configuration.Limits)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	grpcListener, err := net.Listen("tcp", configuration.GRPCAddress)
	if err != nil {
		_ = connection.Close()
		return nil, errors.New("open gRPC listener")
	}
	httpListener, err := net.Listen("tcp", configuration.MetricsAddress)
	if err != nil {
		_ = grpcListener.Close()
		_ = connection.Close()
		return nil, errors.New("open diagnostics listener")
	}
	grpcServer := grpc.NewServer(grpc.Creds(serverCredentials), grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(grpcauth.Policy{
		Service: "answer.v1.AnswerService", DNSName: "edge-api",
	})))
	answerv1.RegisterAnswerServiceServer(grpcServer, answergrpc.NewServer(service))
	return &App{grpcServer: grpcServer, httpServer: &http.Server{Handler: metricRecorder.Handler(), ReadTimeout: 3 * time.Second,
		ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10},
		grpcListener: grpcListener, httpListener: httpListener, connection: connection, service: service, metrics: metricRecorder, log: log}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.log.Info("answer.service.started")
	defer func() {
		_ = a.connection.Close()
		_ = a.log.Sync()
	}()
	a.updateReadiness(ctx)
	go a.probeReadiness(ctx)
	grpcErrors := make(chan error, 1)
	httpErrors := make(chan error, 1)
	go func() { grpcErrors <- a.grpcServer.Serve(a.grpcListener) }()
	go func() { httpErrors <- a.httpServer.Serve(a.httpListener) }()
	select {
	case <-ctx.Done():
		a.shutdown(context.WithoutCancel(ctx))
		a.log.Info("answer.service.stopped")
		return nil
	case err := <-grpcErrors:
		if err != nil {
			return errors.New("gRPC listener failed")
		}
		return nil
	case err := <-httpErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("diagnostics listener failed")
		}
		return nil
	}
}

func (a *App) probeReadiness(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.updateReadiness(ctx)
		}
	}
}

func (a *App) updateReadiness(ctx context.Context) {
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	a.metrics.SetRetrievalReady(a.service.CheckReady(probeContext) == nil)
}

func (a *App) shutdown(ctx context.Context) {
	a.metrics.SetRetrievalReady(false)
	shutdownContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		a.grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownContext.Done():
		a.grpcServer.Stop()
	}
	_ = a.httpServer.Shutdown(shutdownContext)
}
