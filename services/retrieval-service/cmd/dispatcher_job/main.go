package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

const defaultRunAs = 65532

var (
	dropPrivileges = process.DropPrivileges
	newPool        = pgxpool.New
	dialPublisher  = amqp091.Dial
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
	runAs, err := parseRunAs()
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
	connection, err := dialPublisher(uri)
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
	records := repository.NewPostgres(pool)
	publisher := rabbitmq.NewPublisher(channel)
	pending, err := records.PendingOutbox(ctx, 100, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, record := range pending {
		if err = publisher.Publish(ctx, "raglibrarian.retrieval.events.v1", record.EventType, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, Type: record.EventType, MessageId: record.EventID, Body: record.Payload}); err != nil {
			_ = records.DeferOutbox(ctx, record.EventID, time.Now().UTC())
			return errors.New("publish retrieval event")
		}
		if err = records.MarkPublished(ctx, record.EventID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func parseRunAs() (process.Identity, error) {
	uid, err := parseRunAsIdentity("RUN_AS_UID")
	if err != nil {
		return process.Identity{}, err
	}
	gid, err := parseRunAsIdentity("RUN_AS_GID")
	if err != nil {
		return process.Identity{}, err
	}
	return process.Identity{UID: uid, GID: gid}, nil
}

func parseRunAsIdentity(name string) (int, error) {
	value := os.Getenv(name)
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultRunAs, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid process identity")
	}
	return parsed, nil
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
