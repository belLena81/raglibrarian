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
	runtimeMu               sync.Mutex
	sharedRuntime           *bootstrap.Runtime
	errInvalidBrokerMessage = errors.New("invalid broker message")
)

type eventProcessor interface {
	Process(context.Context, application.UploadedEvent) error
	ProcessDeletion(context.Context, application.DeletionEvent) error
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
	if err != nil {
		return err
	}
	if !valid {
		return errInvalidBrokerMessage
	}
	processor, publisher, err := load(ctx)
	if err != nil {
		return err
	}
	return invoke(ctx, event, processor, publisher)
}

type invocation struct {
	upload   *application.UploadedEvent
	deletion *application.DeletionEvent
}

func decodeInvocation(incoming events.RabbitMQEvent) (invocation, bool, error) {
	message, err := singleMessage(incoming)
	if err != nil {
		return invocation{}, false, err
	}
	if message.BasicProperties.ContentType != "application/x-protobuf" || message.BasicProperties.Type == nil || message.BasicProperties.BodySize > 256<<10 {
		return invocation{}, false, nil
	}
	payload, err := base64.StdEncoding.DecodeString(message.Data)
	if err != nil || len(payload) == 0 || len(payload) > 256<<10 {
		return invocation{}, false, nil
	}
	switch *message.BasicProperties.Type {
	case transport.UploadRoute:
		event, decodeErr := transport.DecodeUploaded(payload)
		if decodeErr != nil || !messageIDMatches(message.BasicProperties.MessageID, event.EventID) {
			return invocation{}, false, nil
		}
		return invocation{upload: &event}, true, nil
	case transport.DeletionRoute:
		event, decodeErr := transport.DecodeDeletion(payload)
		if decodeErr != nil || !messageIDMatches(message.BasicProperties.MessageID, event.EventID) {
			return invocation{}, false, nil
		}
		return invocation{deletion: &event}, true, nil
	default:
		return invocation{}, false, nil
	}
}

func invoke(ctx context.Context, event invocation, processor eventProcessor, publisher pendingPublisher) error {
	var processErr error
	if event.upload != nil {
		processErr = processor.Process(ctx, *event.upload)
	} else if event.deletion != nil {
		processErr = processor.ProcessDeletion(ctx, *event.deletion)
	} else {
		processErr = errInvalidBrokerMessage
	}
	publishErr := publisher.PublishPending(ctx)
	if publishErr != nil {
		return errors.Join(processErr, publishErr)
	}
	switch application.DeliveryDisposition(processErr) {
	case application.DeliveryAcknowledge:
		return nil
	case application.DeliveryReject:
		return processErr
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
