// Package rabbitmq contains shared RabbitMQ adapter helpers.
package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

const returnSettleDelay = 10 * time.Millisecond

var (
	// ErrEventReturned identifies a mandatory publish that RabbitMQ could not route.
	ErrEventReturned = errors.New("retrieval event was returned")
	// ErrEventNotConfirmed identifies a publish that RabbitMQ negatively confirmed.
	ErrEventNotConfirmed = errors.New("retrieval event was not confirmed")
)

type confirmingChannel interface {
	NotifyReturn(chan amqp091.Return) chan amqp091.Return
	PublishWithDeferredConfirmWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) (*amqp091.DeferredConfirmation, error)
}

// Publisher serializes mandatory publishes on one RabbitMQ channel.
type Publisher struct {
	channel confirmingChannel
	returns <-chan amqp091.Return
	mu      sync.Mutex
}

// NewPublisher registers the mandatory-return listener before any publish.
func NewPublisher(channel confirmingChannel) *Publisher {
	return &Publisher{
		channel: channel,
		returns: channel.NotifyReturn(make(chan amqp091.Return, 1)),
	}
}

// Publish sends one persistent event and succeeds only after RabbitMQ confirms
// it and no mandatory return is observed for the single in-flight publish.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, message amqp091.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, true, false, message)
	if err != nil {
		return err
	}
	if confirmation == nil {
		return ErrEventNotConfirmed
	}
	return waitMandatoryConfirmation(ctx, p.returns, confirmation.WaitContext)
}

func waitMandatoryConfirmation(ctx context.Context, returns <-chan amqp091.Return, wait func(context.Context) (bool, error)) error {
	confirmed, err := wait(ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrEventNotConfirmed
	}
	select {
	case <-returns:
		return ErrEventReturned
	case <-time.After(returnSettleDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
