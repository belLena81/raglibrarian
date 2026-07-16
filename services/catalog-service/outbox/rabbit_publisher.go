package outbox

import (
	"context"
	"errors"

	"github.com/rabbitmq/amqp091-go"
)

// RabbitPublisher enables confirms and rejects unroutable mandatory messages.
type RabbitPublisher struct {
	channel  *amqp091.Channel
	confirms <-chan amqp091.Confirmation
	returns  <-chan amqp091.Return
}

func NewRabbitPublisher(channel *amqp091.Channel) (*RabbitPublisher, error) {
	if channel == nil {
		return nil, errors.New("rabbit channel is required")
	}
	if err := channel.Confirm(false); err != nil {
		return nil, err
	}
	return &RabbitPublisher{
		channel:  channel,
		confirms: channel.NotifyPublish(make(chan amqp091.Confirmation, 1)),
		returns:  channel.NotifyReturn(make(chan amqp091.Return, 1)),
	}, nil
}

func (p *RabbitPublisher) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	if err := p.channel.PublishWithContext(ctx, exchange, key, mandatory, immediate, message); err != nil {
		return err
	}
	select {
	case returned := <-p.returns:
		return errors.New("broker returned mandatory message: " + returned.ReplyText)
	case confirmation, ok := <-p.confirms:
		if !ok || !confirmation.Ack {
			return errors.New("broker did not confirm message")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
