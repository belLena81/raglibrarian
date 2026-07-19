package config

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type fakeSecrets struct {
	value string
	err   error
}

func (f fakeSecrets) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: &f.value}, nil
}

func TestGetSecretAcceptsOnlyExpectedBoundedField(t *testing.T) {
	value, err := getSecret(context.Background(), fakeSecrets{value: `{"dsn":"postgres://example"}`}, "arn", "database secret", "dsn", 128)
	if err != nil || value != "postgres://example" {
		t.Fatalf("unexpected secret result: %q %v", value, err)
	}
	for _, invalid := range []fakeSecrets{
		{value: `{"dsn":"postgres://example","extra":"value"}`},
		{value: `{"uri":"amqps://example"}`},
		{value: `{"dsn":" leading"}`},
		{err: errors.New("unavailable")},
	} {
		if _, err = getSecret(context.Background(), invalid, "arn", "database secret", "dsn", 128); err == nil {
			t.Fatal("expected fail-closed secret validation")
		}
	}
}

func TestValidateAWSPostgresDSNRequiresVerifiedTLS(t *testing.T) {
	valid := "postgresql://ingestion:encoded%20secret@db.internal:5432/ingestion?sslmode=verify-full"
	if err := validateAWSPostgresDSN(valid); err != nil {
		t.Fatalf("expected verified DSN to pass: %v", err)
	}
	invalid := []string{
		"postgresql://ingestion:secret@db.internal/ingestion",
		"postgresql://ingestion:secret@db.internal/ingestion?sslmode=disable",
		"postgresql://ingestion:secret@db.internal/ingestion?sslmode=require",
		"postgresql://ingestion:secret@db.internal/ingestion?sslmode=verify-ca",
		"postgresql://ingestion@db.internal/ingestion?sslmode=verify-full",
		"postgresql://ingestion:secret@/ingestion?sslmode=verify-full",
	}
	for _, dsn := range invalid {
		if err := validateAWSPostgresDSN(dsn); err == nil {
			t.Fatalf("expected DSN to fail closed: %q", dsn)
		}
	}
}

func TestValidateAWSRabbitURIRequiresAMQPS(t *testing.T) {
	if err := validateAWSRabbitURI("amqps://publisher:encoded%20secret@rabbit.internal:5671/ingestion"); err != nil {
		t.Fatalf("expected AMQPS URI to pass: %v", err)
	}
	invalid := []string{
		"amqp://publisher:secret@rabbit.internal:5672/ingestion",
		"amqps://publisher@rabbit.internal:5671/ingestion",
		"amqps://:secret@rabbit.internal:5671/ingestion",
		"amqps://publisher:secret@:5671/ingestion",
	}
	for _, uri := range invalid {
		if err := validateAWSRabbitURI(uri); err == nil {
			t.Fatalf("expected RabbitMQ URI to fail closed: %q", uri)
		}
	}
}
