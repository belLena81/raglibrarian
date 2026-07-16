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
	channel *amqp091.Channel
	returns <-chan amqp091.Return
	mu      sync.Mutex
}

func NewRabbitPublisher(channel *amqp091.Channel) (*RabbitPublisher, error) {
	if channel == nil {
		return nil, errors.New("rabbit channel is required")
	}
	if err := channel.Confirm(false); err != nil {
		return nil, err
	}
	return &RabbitPublisher{
		channel: channel,
		returns: channel.NotifyReturn(make(chan amqp091.Return, 16)),
	}, nil
}

func (p *RabbitPublisher) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, immediate, message)
	if err != nil {
		return err
	}
	if confirmation == nil {
		return errors.New("broker confirmation unavailable")
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil || !ack {
		return errors.New("broker did not confirm message")
	}
	// RabbitMQ may deliver a mandatory return before or just after its ACK. With
	// one in-flight publish, every buffered return can be correlated safely.
	for {
		select {
		case returned, ok := <-p.returns:
			if !ok {
				return errors.New("broker return channel closed")
			}
			if returned.MessageId == message.MessageId {
				return errors.New("broker returned mandatory message")
			}
		default:
			return nil
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
		connection, err := amqp091.DialConfig(p.uri, amqp091.Config{Dial: func(network, address string) (net.Conn, error) {
			conn, dialErr := dialer.DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			if deadlineErr := conn.SetDeadline(time.Now().Add(5 * time.Second)); deadlineErr != nil {
				_ = conn.Close()
				return nil, deadlineErr
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
