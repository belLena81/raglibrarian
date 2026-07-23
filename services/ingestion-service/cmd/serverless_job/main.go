// serverless_job receives and settles at most one ingestion AMQP delivery.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/serverless"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
	"github.com/rabbitmq/amqp091-go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("ingestion serverless job stopped", "reason", "runtime_failure")
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	if err = config.ValidateServerlessBrokerURI(cfg.RabbitURI); err != nil {
		return err
	}
	invocationContext, invocationCancel := context.WithTimeout(ctx, cfg.JobLease)
	defer invocationCancel()
	runtime, err := bootstrap.New(invocationContext, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	connection, err := transport.DialConsumer(invocationContext, cfg.RabbitURI)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("broker channel unavailable")
	}
	defer func() { _ = channel.Close() }()
	delivery, ok, err := channel.Get(cfg.Queue, false)
	if err != nil {
		return errors.New("broker delivery unavailable")
	}
	if !ok {
		return nil
	}
	message := serverless.Message{ContentType: delivery.ContentType, EventType: delivery.Type, MessageID: delivery.MessageId, Body: delivery.Body}
	if err := serverless.Validate(message); err != nil {
		return rejectInvalid(invocationContext, delivery, err)
	}
	// ProcessOneDelivery is the same bounded retry republish/DLQ path used by
	// the long-running worker. It settles the delivery itself.
	transport.ProcessOneDelivery(invocationContext, delivery, runtime, runtime.Publisher)
	return invocationContext.Err()
}

func rejectInvalid(ctx context.Context, delivery amqp091.Delivery, invokeErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := delivery.Reject(false); err != nil {
		return err
	}
	return invokeErr
}
