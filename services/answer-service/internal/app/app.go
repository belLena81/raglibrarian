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
	"github.com/belLena81/raglibrarian/services/answer-service/internal/retrieval"
	answersruntime "github.com/belLena81/raglibrarian/services/answer-service/internal/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type App struct {
	grpcServer            *grpc.Server
	httpServer            *http.Server
	grpcListener          net.Listener
	httpListener          net.Listener
	connection            *grpc.ClientConn
	service               *application.Service
	metrics               *metrics.Recorder
	log                   *zap.Logger
	readinessProbeTimeout time.Duration
	readinessPollInterval time.Duration
	shutdownTimeout       time.Duration
}

type serverRunner struct {
	name  string
	serve func() error
}

type serverResult struct {
	name string
	err  error
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
	generatorAdapter, err := answersruntime.NewGenerator(configuration.Generator)
	if err != nil {
		return nil, errors.New("configure answer generator")
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
	service, err := application.NewService(retriever, generatorAdapter, diagnostic.New(log, metricRecorder), configuration.Limits, configuration.RequestPolicy)
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
	httpServer := &http.Server{
		Handler:           metricRecorder.Handler(),
		ReadTimeout:       configuration.MetricsReadTimeout,
		ReadHeaderTimeout: configuration.MetricsReadHeaderTimeout,
		WriteTimeout:      configuration.MetricsWriteTimeout,
		IdleTimeout:       configuration.MetricsIdleTimeout,
		MaxHeaderBytes:    configuration.MetricsMaxHeaderBytes,
	}
	return &App{
		grpcServer:            grpcServer,
		httpServer:            httpServer,
		grpcListener:          grpcListener,
		httpListener:          httpListener,
		connection:            connection,
		service:               service,
		metrics:               metricRecorder,
		log:                   log,
		readinessProbeTimeout: configuration.ReadinessProbeTimeout,
		readinessPollInterval: configuration.ReadinessPollInterval,
		shutdownTimeout:       configuration.ShutdownTimeout,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.log.Info("answer.service.started")
	defer func() {
		_ = a.connection.Close()
		_ = a.log.Sync()
	}()

	runContext, cancelRun := context.WithCancel(ctx)
	a.updateReadiness(runContext)
	readinessDone := make(chan struct{})
	go func() {
		defer close(readinessDone)
		a.probeReadiness(runContext, a.readinessPollInterval)
	}()

	err := runServerGroup(ctx, []serverRunner{
		{name: "gRPC", serve: func() error { return a.grpcServer.Serve(a.grpcListener) }},
		{name: "diagnostics", serve: func() error { return a.httpServer.Serve(a.httpListener) }},
	}, func() {
		cancelRun()
		a.shutdown(context.WithoutCancel(ctx))
		<-readinessDone
	}, a.shutdownTimeout)
	if err == nil {
		a.log.Info("answer.service.stopped")
	}
	return err
}

func runServerGroup(ctx context.Context, servers []serverRunner, cleanup func(), joinTimeout time.Duration) error {
	results := make(chan serverResult, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			results <- serverResult{name: server.name, err: server.serve()}
		}()
	}

	var first *serverResult
	select {
	case <-ctx.Done():
	case result := <-results:
		first = &result
	}

	cleanup()
	remaining := len(servers)
	if first != nil {
		remaining--
	}
	timer := time.NewTimer(joinTimeout)
	defer timer.Stop()
	for range remaining {
		select {
		case <-results:
		case <-timer.C:
			return errors.New("answer listeners did not stop after cleanup")
		}
	}
	if first == nil {
		return nil
	}
	if first.err == nil || errors.Is(first.err, http.ErrServerClosed) {
		return errors.New(first.name + " listener stopped unexpectedly")
	}
	return errors.New(first.name + " listener failed")
}

func (a *App) probeReadiness(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
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
	probeContext, cancel := context.WithTimeout(ctx, a.readinessProbeTimeout)
	defer cancel()
	a.metrics.SetRetrievalReady(a.service.CheckReady(probeContext) == nil)
}

func (a *App) shutdown(ctx context.Context) {
	a.metrics.SetRetrievalReady(false)
	shutdownContext, cancel := context.WithTimeout(ctx, a.shutdownTimeout)
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
