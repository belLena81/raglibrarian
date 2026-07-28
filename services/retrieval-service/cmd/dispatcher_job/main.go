package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

var (
	dropPrivileges = process.DropPrivileges
	newPool        = pgxpool.New
	dialPublisher  = rabbitmq.Dial
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	dsn, err := readSecretFile("RETRIEVAL_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return err
	}
	uri, err := readSecretFile("RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE", 4096)
	if err != nil {
		return err
	}
	if err = config.ValidateServerlessBrokerURI(uri); err != nil {
		return err
	}
	runAs, err := config.LoadRunAs()
	if err != nil {
		return err
	}
	if err = dropPrivileges(runAs); err != nil {
		return err
	}
	pool, err := newPool(ctx, dsn)
	if err != nil {
		return errors.New("database unavailable")
	}
	if pool == nil {
		return errors.New("database unavailable")
	}
	defer pool.Close()
	policy, err := config.LoadLambdaRuntimePolicy()
	if err != nil {
		return err
	}
	connection, err := dialPublisher(ctx, uri, rabbitmq.DialPolicy{
		Timeout:   policy.RabbitDialTimeout,
		Heartbeat: policy.RabbitHeartbeat,
	})
	if err != nil {
		return errors.New("publisher unavailable")
	}
	if connection == nil {
		return errors.New("publisher unavailable")
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("publisher unavailable")
	}
	defer func() { _ = channel.Close() }()
	if err = channel.Confirm(false); err != nil {
		return errors.New("enable publisher confirms")
	}
	records := repository.NewPostgres(pool, repository.Policy{FinalizationLease: policy.FinalizationLease})
	publisher := rabbitmq.NewPublisher(channel)
	pending, err := records.PendingOutbox(ctx, 100, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, record := range pending {
		if err = publisher.Publish(ctx, contracts.ExchangeRetrievalEvents, record.EventType, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, Type: record.EventType, MessageId: record.EventID, Body: record.Payload}); err != nil {
			_ = records.DeferOutbox(ctx, record.EventID, time.Now().UTC())
			return errors.New("publish retrieval event")
		}
		if err = records.MarkPublished(ctx, record.EventID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func readSecretFile(key string, maximum int64) (string, error) {
	path := os.Getenv(key)
	if path == "" {
		return "", errors.New("missing secret file")
	}
	file, err := process.OpenSecretFile(path, maximum)
	if err != nil {
		return "", errors.New("invalid secret file")
	}
	defer func() { _ = file.Close() }()
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return "", errors.New("read secret file")
	}
	return strings.TrimSpace(string(value)), nil
}
