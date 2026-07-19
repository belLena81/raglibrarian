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

func main() { lambda.Start(handle) }

func handle(ctx context.Context, incoming events.RabbitMQEvent) error {
	runtimeValue, err := getRuntime(ctx)
	if err != nil {
		return err
	}
	message, err := singleMessage(incoming)
	if err != nil {
		return err
	}
	if message.BasicProperties.ContentType != "application/x-protobuf" || message.BasicProperties.Type == nil || *message.BasicProperties.Type != transport.UploadRoute || message.BasicProperties.BodySize > 256<<10 {
		return errors.New("invalid broker message")
	}
	payload, err := base64.StdEncoding.DecodeString(message.Data)
	if err != nil || len(payload) == 0 || len(payload) > 256<<10 {
		return errors.New("invalid broker message")
	}
	event, err := transport.DecodeUploaded(payload)
	if err != nil {
		return err
	}
	if !messageIDMatches(message.BasicProperties.MessageID, event.EventID) {
		return errors.New("invalid broker message")
	}
	processErr := runtimeValue.Process(ctx, event)
	publishErr := runtimeValue.Outbox.PublishPending(ctx)
	if errors.Is(processErr, application.ErrProcessingDeferred) && publishErr == nil {
		return nil
	}
	return errors.Join(processErr, publishErr)
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
