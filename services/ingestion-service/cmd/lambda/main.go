package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
)

var (
	runtimeMu     sync.Mutex
	sharedRuntime *bootstrap.Runtime
)

type eventProcessor interface {
	Process(context.Context, application.UploadedEvent) error
}

type pendingPublisher interface {
	PublishPending(context.Context) error
}

type invocationLoader func(context.Context) (eventProcessor, pendingPublisher, error)

func main() { lambda.Start(handle) }

func handle(ctx context.Context, incoming events.RabbitMQEvent) error {
	return handleWithLoader(ctx, incoming, loadInvocation)
}

func loadInvocation(ctx context.Context) (eventProcessor, pendingPublisher, error) {
	runtimeValue, err := getRuntime(ctx)
	if err != nil {
		return nil, nil, err
	}
	return runtimeValue, runtimeValue.Outbox, nil
}

func handleWithLoader(ctx context.Context, incoming events.RabbitMQEvent, load invocationLoader) error {
	event, valid, err := decodeInvocation(incoming)
	if err != nil || !valid {
		return err
	}
	processor, publisher, err := load(ctx)
	if err != nil {
		return err
	}
	return invoke(ctx, event, processor, publisher)
}

func decodeInvocation(incoming events.RabbitMQEvent) (application.UploadedEvent, bool, error) {
	message, err := singleMessage(incoming)
	if err != nil {
		return application.UploadedEvent{}, false, err
	}
	if message.BasicProperties.ContentType != "application/x-protobuf" || message.BasicProperties.Type == nil || *message.BasicProperties.Type != transport.UploadRoute || message.BasicProperties.BodySize > 256<<10 {
		return application.UploadedEvent{}, false, nil
	}
	payload, err := base64.StdEncoding.DecodeString(message.Data)
	if err != nil || len(payload) == 0 || len(payload) > 256<<10 {
		return application.UploadedEvent{}, false, nil
	}
	event, err := transport.DecodeUploaded(payload)
	if err != nil {
		return application.UploadedEvent{}, false, nil
	}
	if !messageIDMatches(message.BasicProperties.MessageID, event.EventID) {
		return application.UploadedEvent{}, false, nil
	}
	return event, true, nil
}

func invoke(ctx context.Context, event application.UploadedEvent, processor eventProcessor, publisher pendingPublisher) error {
	processErr := processor.Process(ctx, event)
	publishErr := publisher.PublishPending(ctx)
	if publishErr != nil {
		return errors.Join(processErr, publishErr)
	}
	switch application.DeliveryDisposition(processErr) {
	case application.DeliveryAcknowledge, application.DeliveryReject:
		return nil
	case application.DeliveryRequeue:
		return processErr
	default:
		return processErr
	}
}

func getRuntime(ctx context.Context) (*bootstrap.Runtime, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if sharedRuntime != nil {
		return sharedRuntime, nil
	}
	if os.Geteuid() == 0 {
		return nil, errors.New("lambda runtime must be non-root")
	}
	cfg, err := config.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	runtimeValue, err := bootstrap.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sharedRuntime = runtimeValue
	return sharedRuntime, nil
}

func messageIDMatches(messageID *string, eventID string) bool {
	return messageID != nil && *messageID != "" && *messageID == eventID
}

func singleMessage(incoming events.RabbitMQEvent) (events.RabbitMQMessage, error) {
	if incoming.EventSource != "aws:rmq" || len(incoming.MessagesByQueue) != 1 {
		return events.RabbitMQMessage{}, errors.New("invalid broker batch")
	}
	for _, messages := range incoming.MessagesByQueue {
		if len(messages) != 1 {
			return events.RabbitMQMessage{}, errors.New("invalid broker batch")
		}
		return messages[0], nil
	}
	return events.RabbitMQMessage{}, errors.New("invalid broker batch")
}
