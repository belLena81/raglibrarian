package rabbitmq

import (
	"context"
	"net"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

type DialPolicy struct {
	Timeout   time.Duration
	Heartbeat time.Duration
}

func Dial(ctx context.Context, uri string, policy DialPolicy) (*amqp091.Connection, error) {
	dialer := net.Dialer{Timeout: policy.Timeout}
	return amqp091.DialConfig(uri, amqp091.Config{
		Heartbeat: policy.Heartbeat,
		Dial: func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	})
}
