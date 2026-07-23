package config

import "testing"

func TestValidateServerlessBrokerURI(t *testing.T) {
	for _, value := range []string{"amqps://user:password@rabbit:5671/vhost", "amqps://user:password@10.0.0.5:5671/vhost"} {
		if err := ValidateServerlessBrokerURI(value); err != nil {
			t.Fatalf("ValidateServerlessBrokerURI(%q) = %v", value, err)
		}
	}
	t.Setenv("RETRIEVAL_SERVERLESS_BROKER_ALLOWED_SUFFIXES", "internal.cloud.test")
	if err := ValidateServerlessBrokerURI("amqps://user:password@rabbit.internal.cloud.test:5671/vhost"); err != nil {
		t.Fatalf("ValidateServerlessBrokerURI(private suffix) = %v", err)
	}
	t.Setenv("RETRIEVAL_SERVERLESS_BROKER_ALLOWED_HOSTS", "broker.private.test")
	if err := ValidateServerlessBrokerURI("amqps://user:password@broker.private.test:5671/vhost"); err != nil {
		t.Fatalf("ValidateServerlessBrokerURI(allowed host) = %v", err)
	}
	for _, value := range []string{"amqp://user:password@rabbit.internal:5672/vhost", "amqps://user:password@other.private.test:5671/vhost", "amqps://user:password@rabbit.example.com:5671/vhost"} {
		if err := ValidateServerlessBrokerURI(value); err == nil {
			t.Fatalf("ValidateServerlessBrokerURI(%q) succeeded", value)
		}
	}
}
