// Command rabbitmq-topology renders and imports the repository RabbitMQ
// topology into a shared, single-credential CloudAMQP test vhost.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maximumFileBytes = 1 << 20

const (
	catalogRetrievalDLX      = "raglibrarian.retrieval.events.dlx.v1"
	catalogRetrievalDLQ      = "catalog.retrieval-terminal.dlq.v1"
	catalogRetrievalRouteKey = "catalog.retrieval-terminal.v1"
)

var quorumOnlyArguments = map[string]struct{}{
	"x-queue-type":           {},
	"x-delivery-limit":       {},
	"x-dead-letter-strategy": {},
}

var applicationRetryBoundedQueues = map[string]struct{}{
	"catalog.book-processing.v1":    {},
	"catalog.retrieval-terminal.v1": {},
	"ingestion.book-uploaded.v1":    {},
	"retrieval.book-uploaded.v1":    {},
	"retrieval.chunks-ready.v1":     {},
	"retrieval.index-batch.v1":      {},
}

type definitions struct {
	Exchanges []map[string]any `json:"exchanges"`
	Queues    []map[string]any `json:"queues"`
	Bindings  []map[string]any `json:"bindings"`
}

type options struct {
	URIFile           string
	DefinitionsFile   string
	ManagementBaseURL string
	RenderOutput      string
}

func main() {
	var configuration options
	flag.StringVar(&configuration.URIFile, "uri-file", "", "owner-only file containing the provider AMQPS URI")
	flag.StringVar(&configuration.DefinitionsFile, "definitions", "", "owner-only repository definitions JSON")
	flag.StringVar(&configuration.ManagementBaseURL, "management-base-url", "", "optional HTTPS RabbitMQ management API base URL")
	flag.StringVar(&configuration.RenderOutput, "render-output", "", "write transformed topology instead of importing it")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("unexpected positional arguments"))
	}
	if err := run(context.Background(), configuration); err != nil {
		fail(err)
	}
}

func fail(_ error) {
	fmt.Fprintln(os.Stderr, "RabbitMQ topology operation failed")
	os.Exit(1)
}

func run(ctx context.Context, configuration options) error {
	if configuration.URIFile == "" || configuration.DefinitionsFile == "" {
		return errors.New("URI and definitions files are required")
	}
	uriValue, err := readOwnerOnlyFile(configuration.URIFile, 4096)
	if err != nil {
		return err
	}
	provider, err := parseProviderURI(strings.TrimSpace(string(uriValue)))
	if err != nil {
		return err
	}
	source, err := readOwnerOnlyFile(configuration.DefinitionsFile, maximumFileBytes)
	if err != nil {
		return err
	}
	rendered, err := renderDefinitions(source, provider.VHost)
	if err != nil {
		return err
	}
	if configuration.RenderOutput != "" {
		return writeOwnerOnlyFile(configuration.RenderOutput, rendered)
	}
	return importDefinitions(ctx, provider, configuration.ManagementBaseURL, rendered)
}

func readOwnerOnlyFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- operator-controlled deployment file.
	if err != nil {
		return nil, errors.New("open deployment file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > maximum {
		return nil, errors.New("deployment file is not a bounded owner-only regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) > maximum {
		return nil, errors.New("read deployment file")
	}
	return contents, nil
}

func writeOwnerOnlyFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400) // #nosec G304 -- operator-controlled output path; overwrite is forbidden.
	if err != nil {
		return errors.New("create topology output")
	}
	defer func() { _ = file.Close() }()
	if _, err = file.Write(contents); err != nil {
		return errors.New("write topology output")
	}
	return file.Sync()
}

type providerURI struct {
	Host     string
	Username string
	Password string
	VHost    string
}

func parseProviderURI(value string) (providerURI, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "amqps" || parsed.Hostname() == "" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return providerURI{}, errors.New("invalid provider AMQPS URI")
	}
	password, present := parsed.User.Password()
	vhost, unescapeErr := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if !present || parsed.User.Username() == "" || password == "" || unescapeErr != nil || vhost == "" || strings.ContainsAny(vhost, "\r\n") {
		return providerURI{}, errors.New("invalid provider AMQPS URI")
	}
	return providerURI{Host: parsed.Hostname(), Username: parsed.User.Username(), Password: password, VHost: vhost}, nil
}

func renderDefinitions(source []byte, vhost string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	var topology definitions
	if err := decoder.Decode(&topology); err != nil {
		return nil, errors.New("decode RabbitMQ definitions")
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	for _, collection := range [][]map[string]any{topology.Exchanges, topology.Queues, topology.Bindings} {
		for _, item := range collection {
			item["vhost"] = vhost
		}
	}
	if containsNamed(topology.Exchanges, catalogRetrievalDLX) && containsNamed(topology.Queues, catalogRetrievalDLQ) {
		topology.Bindings = append(topology.Bindings, map[string]any{
			"source":           catalogRetrievalDLX,
			"destination":      catalogRetrievalDLQ,
			"destination_type": "queue",
			"routing_key":      catalogRetrievalRouteKey,
			"vhost":            vhost,
			"arguments":        map[string]any{},
		})
	}
	for _, queue := range topology.Queues {
		arguments, ok := queue["arguments"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid queue arguments")
		}
		if _, hasDeliveryLimit := arguments["x-delivery-limit"]; hasDeliveryLimit {
			name, nameOK := queue["name"].(string)
			if !nameOK {
				return nil, errors.New("invalid queue name")
			}
			if _, protected := applicationRetryBoundedQueues[name]; !protected {
				return nil, errors.New("delivery-limited queue lacks an application retry bound")
			}
		}
		for argument := range quorumOnlyArguments {
			delete(arguments, argument)
		}
	}
	dynamicQueues := make(map[string]struct{})
	for _, queue := range topology.Queues {
		name, _ := queue["name"].(string)
		if name == "edge.book-status.local" || strings.HasPrefix(name, "edge.book-status.local.") {
			dynamicQueues[name] = struct{}{}
		}
	}
	topology.Queues = filterDynamicQueues(topology.Queues, dynamicQueues)
	topology.Bindings = filterDynamicBindings(topology.Bindings, dynamicQueues)
	var err error
	if topology.Exchanges, err = uniqueNamed(topology.Exchanges, "exchange"); err != nil {
		return nil, err
	}
	if topology.Queues, err = uniqueNamed(topology.Queues, "queue"); err != nil {
		return nil, err
	}
	if topology.Bindings, err = uniqueBindings(topology.Bindings); err != nil {
		return nil, err
	}
	rendered, err := json.Marshal(topology)
	if err != nil {
		return nil, errors.New("encode RabbitMQ definitions")
	}
	return append(rendered, '\n'), nil
}

func containsNamed(items []map[string]any, name string) bool {
	for _, item := range items {
		if item["name"] == name {
			return true
		}
	}
	return false
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("RabbitMQ definitions contain trailing data")
	}
	return nil
}

func filterDynamicQueues(items []map[string]any, dynamic map[string]struct{}) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		if _, excluded := dynamic[name]; !excluded {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterDynamicBindings(items []map[string]any, dynamic map[string]struct{}) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		destination, _ := item["destination"].(string)
		if _, excluded := dynamic[destination]; !excluded {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func uniqueNamed(items []map[string]any, kind string) ([]map[string]any, error) {
	seen := make(map[string][]byte)
	unique := make([]map[string]any, 0, len(items))
	for _, item := range items {
		name, ok := item["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid %s name", kind)
		}
		canonical, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encode %s", kind)
		}
		if previous, exists := seen[name]; exists {
			if !bytes.Equal(previous, canonical) {
				return nil, fmt.Errorf("conflicting duplicate %s", kind)
			}
			continue
		}
		seen[name] = canonical
		unique = append(unique, item)
	}
	return unique, nil
}

func uniqueBindings(items []map[string]any) ([]map[string]any, error) {
	seen := make(map[string][]byte)
	unique := make([]map[string]any, 0, len(items))
	for _, item := range items {
		source, sourceOK := item["source"].(string)
		destination, destinationOK := item["destination"].(string)
		destinationType, typeOK := item["destination_type"].(string)
		routingKey, routeOK := item["routing_key"].(string)
		if !sourceOK || !destinationOK || !typeOK || !routeOK || source == "" || destination == "" || (destinationType != "queue" && destinationType != "exchange") {
			return nil, errors.New("invalid binding")
		}
		key := strings.Join([]string{source, destination, destinationType, routingKey}, "\x00")
		canonical, err := json.Marshal(item)
		if err != nil {
			return nil, errors.New("encode binding")
		}
		if previous, exists := seen[key]; exists {
			if !bytes.Equal(previous, canonical) {
				return nil, errors.New("conflicting duplicate binding")
			}
			continue
		}
		seen[key] = canonical
		unique = append(unique, item)
	}
	return unique, nil
}

func importDefinitions(parent context.Context, provider providerURI, managementBaseURL string, payload []byte) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if managementBaseURL == "" {
		managementBaseURL = "https://" + provider.Host + "/api"
	}
	parsedBase, err := url.Parse(managementBaseURL)
	if err != nil || parsedBase.Scheme != "https" || parsedBase.Host == "" || parsedBase.User != nil || parsedBase.RawQuery != "" || parsedBase.Fragment != "" {
		return errors.New("invalid RabbitMQ management base URL")
	}
	endpoint := strings.TrimRight(managementBaseURL, "/") + "/definitions/" + url.PathEscape(provider.VHost)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("create topology request")
	}
	request.SetBasicAuth(provider.Username, provider.Password)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "raglibrarian-cloud-bootstrap/1")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request) // #nosec G704 -- endpoint is derived from the operator-owned provider URI.
	if err != nil {
		return errors.New("broker management endpoint unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return errors.New("broker topology import rejected")
	}
	return nil
}
