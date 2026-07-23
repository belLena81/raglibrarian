// serverless_job receives and settles at most one retrieval AMQP delivery.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/serverless"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/worker"
	"github.com/rabbitmq/amqp091-go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		worker.LogFailure()
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	queue := os.Getenv("RETRIEVAL_SERVERLESS_QUEUE")
	if !validQueue(queue) {
		return errors.New("invalid serverless queue")
	}
	if err = config.ValidateServerlessBrokerURI(cfg.ConsumerRabbitURI); err != nil {
		return err
	}
	if err = config.ValidateServerlessBrokerURI(cfg.PublisherRabbitURI); err != nil {
		return err
	}
	invocationContext, invocationCancel := context.WithTimeout(ctx, cfg.ServerlessInvocationTimeout)
	defer invocationCancel()
	runtime, err := worker.New(invocationContext, cfg, diagnostic.New(logger.Must("retrieval-serverless-job")))
	if err != nil {
		return err
	}
	defer runtime.Close()
	connection, err := amqp091.Dial(cfg.ConsumerRabbitURI)
	if err != nil {
		return errors.New("broker unavailable")
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("broker channel unavailable")
	}
	defer func() { _ = channel.Close() }()
	publisherConnection, err := amqp091.Dial(cfg.PublisherRabbitURI)
	if err != nil {
		return errors.New("publisher unavailable")
	}
	defer func() { _ = publisherConnection.Close() }()
	publisherChannel, err := publisherConnection.Channel()
	if err != nil {
		return errors.New("publisher unavailable")
	}
	defer func() { _ = publisherChannel.Close() }()
	if err = publisherChannel.Confirm(false); err != nil {
		return errors.New("enable publisher confirms")
	}
	delivery, ok, err := channel.Get(queue, false)
	if err != nil {
		return errors.New("broker delivery unavailable")
	}
	if !ok {
		return nil
	}
	if err = serverless.Validate(serverless.Message{Queue: queue, ContentType: delivery.ContentType, EventType: delivery.Type, MessageID: delivery.MessageId, Body: delivery.Body}); err != nil {
		return rejectInvalid(invocationContext, delivery, err)
	}
	return runtime.ProcessOneDelivery(invocationContext, rabbitmq.NewPublisher(publisherChannel), queue, delivery)
}

func validQueue(queue string) bool {
	return queue == serverless.MetadataQueue || queue == serverless.ManifestQueue || queue == serverless.IndexQueue || queue == serverless.LifecycleQueue
}

func rejectInvalid(ctx context.Context, delivery amqp091.Delivery, invokeErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := delivery.Nack(false, false); err != nil {
		return err
	}
	return invokeErr
}
