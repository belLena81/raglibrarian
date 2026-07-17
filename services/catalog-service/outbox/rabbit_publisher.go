package outbox

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// RabbitPublisher enables confirms and rejects unroutable mandatory messages.
type RabbitPublisher struct {
	channel       *amqp091.Channel
	returns       <-chan amqp091.Return
	confirmations <-chan amqp091.Confirmation
	mu            sync.Mutex
}

func NewRabbitPublisher(channel *amqp091.Channel) (*RabbitPublisher, error) {
	if channel == nil {
		return nil, errors.New("rabbit channel is required")
	}
	if err := channel.Confirm(false); err != nil {
		return nil, err
	}
	return &RabbitPublisher{
		channel:       channel,
		returns:       channel.NotifyReturn(make(chan amqp091.Return, 1)),
		confirmations: channel.NotifyPublish(make(chan amqp091.Confirmation, 1)),
	}, nil
}

func (p *RabbitPublisher) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	expectedTag := p.channel.GetNextPublishSeqNo()
	if err := p.channel.PublishWithContext(ctx, exchange, key, mandatory, immediate, message); err != nil {
		return err
	}
	return p.awaitRoutedConfirmation(ctx, expectedTag, message.MessageId)
}

// awaitRoutedConfirmation consumes return and confirmation notifications from
// the same single-inflight channel. amqp091-go dispatches basic.return before
// a later basic.ack on that channel; draining returns before accepting the ACK
// keeps an unroutable message from being marked published.
func (p *RabbitPublisher) awaitRoutedConfirmation(ctx context.Context, expectedTag uint64, messageID string) error {
	for {
		if returned, ok := p.drainReturn(messageID); !ok {
			return errors.New("broker return channel closed")
		} else if returned {
			return errors.New("broker returned mandatory message")
		}
		select {
		case _, ok := <-p.returns:
			if !ok {
				return errors.New("broker return channel closed")
			}
			return errors.New("broker returned mandatory message")
		case confirmation, ok := <-p.confirmations:
			if !ok {
				return errors.New("broker confirmation channel closed")
			}
			if confirmation.DeliveryTag != expectedTag {
				return errors.New("broker confirmation order invalid")
			}
			if !confirmation.Ack {
				return errors.New("broker did not confirm message")
			}
			if returned, ok := p.drainReturn(messageID); !ok {
				return errors.New("broker return channel closed")
			} else if returned {
				return errors.New("broker returned mandatory message")
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *RabbitPublisher) drainReturn(messageID string) (returned bool, open bool) {
	for {
		select {
		case _, ok := <-p.returns:
			if !ok {
				return false, false
			}
			// A single-inflight channel must never receive a return for another
			// message. Reject it rather than silently accepting a protocol-order
			// violation and potentially marking the current event published.
			return true, true
		default:
			return false, true
		}
	}
}

// ReconnectingPublisher keeps the durable outbox available while RabbitMQ is
// down. Connections are created lazily and discarded after every failure.
type ReconnectingPublisher struct {
	uri        string
	mu         sync.Mutex
	connection *amqp091.Connection
	publisher  *RabbitPublisher
}

func NewReconnectingPublisher(uri string) *ReconnectingPublisher {
	return &ReconnectingPublisher{uri: uri}
}

func (p *ReconnectingPublisher) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.publisher == nil {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		connection, err := amqp091.DialConfig(p.uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) {
			conn, dialErr := dialer.DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			return conn, nil
		}})
		if err != nil {
			return errors.New("broker unavailable")
		}
		channel, err := connection.Channel()
		if err != nil {
			_ = connection.Close()
			return errors.New("broker unavailable")
		}
		publisher, err := NewRabbitPublisher(channel)
		if err != nil {
			_ = channel.Close()
			_ = connection.Close()
			return errors.New("broker unavailable")
		}
		p.connection, p.publisher = connection, publisher
	}
	err := p.publisher.PublishWithContext(ctx, exchange, key, mandatory, immediate, message)
	if err != nil {
		_ = p.closeLocked()
	}
	return err
}

func (p *ReconnectingPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
}
func (p *ReconnectingPublisher) closeLocked() error {
	if p.connection == nil {
		return nil
	}
	err := p.connection.Close()
	p.connection, p.publisher = nil, nil
	return err
}
