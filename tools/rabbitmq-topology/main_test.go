package main

import (
	"encoding/json"
	"testing"
)

func TestRenderDefinitionsStripsSharedPlanIncompatibilities(t *testing.T) {
	source := []byte(`{
		"users":[{"name":"must-not-leak"}],
		"permissions":[{"user":"must-not-leak"}],
		"vhosts":[{"name":"/"}],
		"exchanges":[{"name":"events","vhost":"/","type":"topic","arguments":{}},{"name":"events","vhost":"/","type":"topic","arguments":{}}],
		"queues":[
			{"name":"catalog.book-processing.v1","vhost":"/","arguments":{"x-queue-type":"quorum","x-delivery-limit":5,"x-dead-letter-strategy":"at-least-once","x-message-ttl":5000}},
			{"name":"edge.book-status.local.1","vhost":"/","arguments":{}}
		],
		"bindings":[
			{"source":"events","destination":"catalog.book-processing.v1","destination_type":"queue","routing_key":"jobs","vhost":"/","arguments":{}},
			{"source":"events","destination":"edge.book-status.local.1","destination_type":"queue","routing_key":"status","vhost":"/","arguments":{}}
		]
	}`)

	rendered, err := renderDefinitions(source, "assigned-vhost")
	if err != nil {
		t.Fatal(err)
	}
	var result definitions
	if err = json.Unmarshal(rendered, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Exchanges) != 1 || len(result.Queues) != 1 || len(result.Bindings) != 1 {
		t.Fatalf("rendered topology sizes = exchanges:%d queues:%d bindings:%d", len(result.Exchanges), len(result.Queues), len(result.Bindings))
	}
	if result.Queues[0]["vhost"] != "assigned-vhost" {
		t.Fatalf("queue vhost = %v", result.Queues[0]["vhost"])
	}
	arguments := result.Queues[0]["arguments"].(map[string]any)
	for argument := range quorumOnlyArguments {
		if _, present := arguments[argument]; present {
			t.Fatalf("quorum-only argument retained: %s", argument)
		}
	}
	if arguments["x-message-ttl"] != float64(5000) {
		t.Fatalf("portable queue arguments = %#v", arguments)
	}
}

func TestRenderDefinitionsRejectsUnknownDeliveryLimitedQueue(t *testing.T) {
	source := []byte(`{
		"exchanges":[],
		"queues":[
			{"name":"unknown.jobs.v1","vhost":"/","arguments":{"x-queue-type":"quorum","x-delivery-limit":5}}
		],
		"bindings":[]
	}`)

	if _, err := renderDefinitions(source, "assigned-vhost"); err == nil {
		t.Fatal("unknown delivery-limited queue was accepted")
	}
}

func TestRenderDefinitionsAcceptsApplicationRetryBoundedQueues(t *testing.T) {
	queueNames := []string{
		"catalog.book-processing.v1",
		"catalog.retrieval-terminal.v1",
		"ingestion.book-uploaded.v1",
		"retrieval.book-uploaded.v1",
		"retrieval.chunks-ready.v1",
		"retrieval.index-batch.v1",
	}
	for _, queueName := range queueNames {
		t.Run(queueName, func(t *testing.T) {
			source, err := json.Marshal(definitions{
				Queues: []map[string]any{{
					"name":      queueName,
					"vhost":     "/",
					"arguments": map[string]any{"x-delivery-limit": 5},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = renderDefinitions(source, "assigned-vhost"); err != nil {
				t.Fatalf("application-bounded queue rejected: %v", err)
			}
		})
	}
}

func TestRenderDefinitionsAddsCatalogRetrievalTerminalDLQBinding(t *testing.T) {
	source := []byte(`{
		"exchanges":[{"name":"raglibrarian.retrieval.events.dlx.v1","vhost":"/","type":"topic","arguments":{}}],
		"queues":[{"name":"catalog.retrieval-terminal.dlq.v1","vhost":"/","arguments":{}}],
		"bindings":[]
	}`)

	rendered, err := renderDefinitions(source, "assigned-vhost")
	if err != nil {
		t.Fatal(err)
	}
	var result definitions
	if err = json.Unmarshal(rendered, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Bindings) != 1 {
		t.Fatalf("binding count = %d", len(result.Bindings))
	}
	binding := result.Bindings[0]
	if binding["source"] != "raglibrarian.retrieval.events.dlx.v1" ||
		binding["destination"] != "catalog.retrieval-terminal.dlq.v1" ||
		binding["destination_type"] != "queue" ||
		binding["routing_key"] != "catalog.retrieval-terminal.v1" ||
		binding["vhost"] != "assigned-vhost" {
		t.Fatalf("terminal DLQ binding = %#v", binding)
	}
	arguments, ok := binding["arguments"].(map[string]any)
	if !ok || len(arguments) != 0 {
		t.Fatalf("terminal DLQ binding arguments = %#v", binding["arguments"])
	}
}

func TestRenderDefinitionsDeduplicatesCatalogRetrievalTerminalDLQBinding(t *testing.T) {
	source := []byte(`{
		"exchanges":[{"name":"raglibrarian.retrieval.events.dlx.v1","vhost":"/","type":"topic","arguments":{}}],
		"queues":[{"name":"catalog.retrieval-terminal.dlq.v1","vhost":"/","arguments":{}}],
		"bindings":[{
			"source":"raglibrarian.retrieval.events.dlx.v1",
			"destination":"catalog.retrieval-terminal.dlq.v1",
			"destination_type":"queue",
			"routing_key":"catalog.retrieval-terminal.v1",
			"vhost":"/",
			"arguments":{}
		}]
	}`)

	rendered, err := renderDefinitions(source, "assigned-vhost")
	if err != nil {
		t.Fatal(err)
	}
	var result definitions
	if err = json.Unmarshal(rendered, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Bindings) != 1 {
		t.Fatalf("binding count = %d", len(result.Bindings))
	}
}

func TestRenderDefinitionsRejectsConflictingCatalogRetrievalTerminalDLQBinding(t *testing.T) {
	source := []byte(`{
		"exchanges":[{"name":"raglibrarian.retrieval.events.dlx.v1","vhost":"/","type":"topic","arguments":{}}],
		"queues":[{"name":"catalog.retrieval-terminal.dlq.v1","vhost":"/","arguments":{}}],
		"bindings":[{
			"source":"raglibrarian.retrieval.events.dlx.v1",
			"destination":"catalog.retrieval-terminal.dlq.v1",
			"destination_type":"queue",
			"routing_key":"catalog.retrieval-terminal.v1",
			"vhost":"/",
			"arguments":{"unexpected":true}
		}]
	}`)

	if _, err := renderDefinitions(source, "assigned-vhost"); err == nil {
		t.Fatal("conflicting terminal DLQ binding was accepted")
	}
}

func TestRenderDefinitionsRejectsConflictingDuplicates(t *testing.T) {
	source := []byte(`{
		"exchanges":[{"name":"events","vhost":"/","type":"topic","arguments":{}},{"name":"events","vhost":"/","type":"direct","arguments":{}}],
		"queues":[],"bindings":[]
	}`)
	if _, err := renderDefinitions(source, "assigned-vhost"); err == nil {
		t.Fatal("conflicting duplicate exchange was accepted")
	}
}

func TestParseProviderURI(t *testing.T) {
	parsed, err := parseProviderURI("amqps://user:p%40ss@example.com/vhost%2Ftest")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "example.com" || parsed.Username != "user" || parsed.Password != "p@ss" || parsed.VHost != "vhost/test" {
		t.Fatalf("parsed provider URI = %#v", parsed)
	}
	for _, value := range []string{"amqp://user:pass@example.com/vhost", "amqps://example.com/vhost", "amqps://user:pass@example.com/"} {
		if _, err = parseProviderURI(value); err == nil {
			t.Fatalf("invalid URI accepted: %s", value)
		}
	}
}
