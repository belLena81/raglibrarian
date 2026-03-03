// RabbitMQ message types and publisher/consumer helpers.
// Shared by all services and Lambdas that produce or consume events.

module github.com/belLena81/raglibrarian/pkg/events

go 1.26

require (
    github.com/rabbitmq/amqp091-go                    v1.10.0
    github.com/belLena81/raglibrarian/pkg/proto        v0.0.0
)

replace github.com/belLena81/raglibrarian/pkg/proto => ../proto