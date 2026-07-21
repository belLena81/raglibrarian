// Package lambdaadapter exposes the same application use cases through AWS Lambda events.
package lambdaadapter

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/storage"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/transport"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

type Secret struct {
	PostgresDSN, PublisherRabbitURI, ArtifactBucket, Region, TEIURL, QdrantURL, QdrantAPIKey string
}

func (s *Secret) UnmarshalJSON(payload []byte) error {
	var values map[string]string
	if err := json.Unmarshal(payload, &values); err != nil {
		return err
	}
	s.PostgresDSN = values["postgres_dsn"]
	s.PublisherRabbitURI = values["publisher_rabbitmq_uri"]
	s.ArtifactBucket = values["artifact_bucket"]
	s.Region = values["aws_region"]
	s.TEIURL = values["tei_url"]
	s.QdrantURL = values["qdrant_url"]
	s.QdrantAPIKey = values["qdrant_api_key"]
	return nil
}

type Runtime struct {
	repository    *repository.Postgres
	manifestFails manifestFailureRecorder
	batchFails    batchFailureRecorder
	vectorJobs    vectorCleanupRepository
	objects       storage.ObjectStore
	planner       *application.Planner
	indexer       batchProcessor
	vector        vectorDeactivator
	secret        Secret
}

var errManifestArtifactRead = errors.New("manifest artifact read failed")

type manifestFailureRecorder interface {
	FailManifest(context.Context, application.ManifestEvent, domain.FailureCategory, time.Time) error
}

type batchFailureRecorder interface {
	FailBatch(context.Context, application.BatchWork, domain.FailureCategory, time.Time) error
}

type batchProcessor interface {
	Process(context.Context, application.BatchWork) error
}

type vectorCleanupRepository interface {
	PendingVectorCleanup(context.Context, int, time.Time) ([]repository.VectorCleanupJob, error)
	CompleteVectorCleanup(context.Context, string) error
	RetryVectorCleanup(context.Context, string, time.Time) error
}

type vectorDeactivator interface {
	DeactivateJob(context.Context, string) error
}

func validateMode() error {
	if os.Getenv("RETRIEVAL_PROCESSING_MODE") != "lambda" || os.Getenv("RETRIEVAL_RUNTIME_BACKEND") != "aws" || os.Getenv("RETRIEVAL_INDEX_PROFILE") != "m5-jina-code-v1" {
		return errors.New("invalid Lambda processing mode")
	}
	return nil
}

func NewPlannerRuntime(ctx context.Context) (*Runtime, error) {
	if err := validateMode(); err != nil {
		return nil, err
	}
	secret, err := loadSecret(ctx, os.Getenv("RETRIEVAL_RUNTIME_SECRET_ARN"))
	if err != nil {
		return nil, err
	}
	if secret.PostgresDSN == "" || secret.Region == "" || secret.ArtifactBucket == "" {
		return nil, errors.New("invalid planner runtime secret")
	}
	pool, err := pgxpool.New(ctx, secret.PostgresDSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	records := repository.NewPostgres(pool)
	objects, err := storage.NewAWS(ctx, secret.Region, secret.ArtifactBucket)
	if err != nil {
		pool.Close()
		return nil, err
	}
	planner, err := application.NewPlanner(records, randomID, time.Now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{repository: records, manifestFails: records, objects: objects, planner: planner, secret: secret}, nil
}

func NewIndexerRuntime(ctx context.Context) (*Runtime, error) {
	if err := validateMode(); err != nil {
		return nil, err
	}
	secret, err := loadSecret(ctx, os.Getenv("RETRIEVAL_RUNTIME_SECRET_ARN"))
	if err != nil || secret.PostgresDSN == "" || secret.Region == "" || secret.ArtifactBucket == "" || secret.TEIURL == "" || secret.QdrantURL == "" || secret.QdrantAPIKey == "" {
		return nil, errors.New("invalid indexer runtime secret")
	}
	if err = validatePrivateEndpoint(ctx, secret.TEIURL); err != nil {
		return nil, errors.New("invalid private embedding endpoint")
	}
	if err = validatePrivateEndpoint(ctx, secret.QdrantURL); err != nil {
		return nil, errors.New("invalid private vector endpoint")
	}
	pool, err := pgxpool.New(ctx, secret.PostgresDSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	records := repository.NewPostgres(pool)
	objects, err := storage.NewAWS(ctx, secret.Region, secret.ArtifactBucket)
	if err != nil {
		pool.Close()
		return nil, err
	}
	httpClient := &http.Client{Timeout: 90 * time.Second, CheckRedirect: rejectRedirect}
	embedder, err := embedding.NewTEI(secret.TEIURL, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	index, err := vector.NewAuthenticatedQdrant(secret.QdrantURL, "evidence_v2", secret.QdrantAPIKey, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	collectionContext, collectionCancel := context.WithTimeout(ctx, 10*time.Second)
	err = index.EnsureCollection(collectionContext)
	collectionCancel()
	if err != nil {
		pool.Close()
		return nil, errors.New("initialize vector collection")
	}
	reader, _ := artifact.NewReader(objects)
	indexer, err := application.NewIndexer(records, reader, embedder, index, time.Now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{repository: records, manifestFails: records, batchFails: records, vectorJobs: records, objects: objects, indexer: indexer, vector: index, secret: secret}, nil
}

func NewDispatcherRuntime(ctx context.Context) (*Runtime, error) {
	return newDatabaseRuntime(ctx, true)
}

func NewCleanupRuntime(ctx context.Context) (*Runtime, error) {
	if err := validateMode(); err != nil {
		return nil, err
	}
	secret, err := loadSecret(ctx, os.Getenv("RETRIEVAL_RUNTIME_SECRET_ARN"))
	if err != nil || secret.PostgresDSN == "" || secret.QdrantURL == "" || secret.QdrantAPIKey == "" {
		return nil, errors.New("invalid cleanup runtime secret")
	}
	if err = validatePrivateEndpoint(ctx, secret.QdrantURL); err != nil {
		return nil, errors.New("invalid private vector endpoint")
	}
	pool, err := pgxpool.New(ctx, secret.PostgresDSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	records := repository.NewPostgres(pool)
	httpClient := &http.Client{Timeout: 90 * time.Second, CheckRedirect: rejectRedirect}
	index, err := vector.NewAuthenticatedQdrant(secret.QdrantURL, "evidence_v2", secret.QdrantAPIKey, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{repository: records, manifestFails: records, vectorJobs: records, vector: index, secret: secret}, nil
}

func newDatabaseRuntime(ctx context.Context, publisher bool) (*Runtime, error) {
	if err := validateMode(); err != nil {
		return nil, err
	}
	secret, err := loadSecret(ctx, os.Getenv("RETRIEVAL_RUNTIME_SECRET_ARN"))
	if err != nil || secret.PostgresDSN == "" || (publisher && secret.PublisherRabbitURI == "") {
		return nil, errors.New("invalid database runtime secret")
	}
	if publisher {
		if err = validatePrivateBroker(ctx, secret.PublisherRabbitURI); err != nil {
			return nil, errors.New("invalid private publisher endpoint")
		}
	}
	pool, err := pgxpool.New(ctx, secret.PostgresDSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	records := repository.NewPostgres(pool)
	return &Runtime{repository: records, manifestFails: records, secret: secret}, nil
}

type RabbitEvent struct {
	Messages map[string][]RabbitMessage `json:"rmqMessagesByQueue"`
}

type RabbitMessage struct {
	Data            string                `json:"data"`
	BasicProperties RabbitBasicProperties `json:"basicProperties"`
}

type RabbitBasicProperties struct {
	Headers map[string]any `json:"headers"`
}

func (r *Runtime) Plan(ctx context.Context, event RabbitEvent) error {
	queue, payload, err := oneMessage(event)
	if err != nil {
		return err
	}
	if queueContains(queue, "retrieval.book-uploaded.v1") {
		metadata, decodeErr := transport.DecodeMetadata(payload)
		if decodeErr != nil {
			return decodeErr
		}
		return r.planner.HandleMetadata(ctx, metadata)
	}
	if !queueContains(queue, "retrieval.chunks-ready.v1") {
		return application.ErrInvalidEvent
	}
	manifest, err := transport.DecodeManifestEnvelope(payload)
	if err != nil {
		return err
	}
	manifestPayload, err := r.objects.ReadBounded(ctx, manifest.ManifestReference, 4<<20)
	if err != nil {
		if eventAttempt(event) >= 4 {
			return r.recordManifestArtifactRead(ctx, payload, errors.Join(errManifestArtifactRead, err))
		}
		return err
	}
	manifest, err = transport.DecodeManifest(payload, manifestPayload)
	if err != nil {
		if category, terminal := application.ManifestFailureCategory(manifest, err); terminal {
			return r.manifestFailureRecorder().FailManifest(ctx, manifest, category, time.Now().UTC())
		}
		return err
	}
	return r.planner.HandleManifest(ctx, manifest)
}

func (r *Runtime) recordManifestArtifactRead(ctx context.Context, payload []byte, err error) error {
	if !errors.Is(err, errManifestArtifactRead) {
		return err
	}
	event, decodeErr := transport.DecodeManifestEnvelope(payload)
	if decodeErr != nil {
		return decodeErr
	}
	return r.manifestFailureRecorder().FailManifest(ctx, event, domain.FailureManifestIntegrity, time.Now().UTC())
}

func (r *Runtime) Index(ctx context.Context, event RabbitEvent) error {
	_, payload, err := oneMessage(event)
	if err != nil {
		return err
	}
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	if err = r.indexer.Process(ctx, work); err != nil {
		if application.TerminalIndexingFailure(err) || eventAttempt(event) >= 4 {
			if failureErr := r.batchFailureRecorder().FailBatch(ctx, work, application.FailureCategory(err), time.Now().UTC()); failureErr != nil {
				return errors.New("record terminal indexing failure")
			}
			if r.vector != nil {
				if cleanupErr := r.vector.DeactivateJob(ctx, work.JobID); cleanupErr == nil {
					if completeErr := r.vectorCleanupRepository().CompleteVectorCleanup(ctx, work.JobID); completeErr != nil {
						return errors.New("complete vector cleanup")
					}
				}
			}
			return nil
		}
		return err
	}
	return nil
}

func (r *Runtime) Dispatch(ctx context.Context) error {
	connection, err := dial(ctx, r.secret.PublisherRabbitURI)
	if err != nil {
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
	publisher := rabbitmq.NewPublisher(channel)
	records, err := r.repository.PendingOutbox(ctx, 100, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, record := range records {
		publishErr := publisher.Publish(ctx, "raglibrarian.retrieval.events.v1", record.EventType, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, Type: record.EventType, MessageId: record.EventID, Body: record.Payload})
		if publishErr != nil {
			_ = r.repository.DeferOutbox(ctx, record.EventID, time.Now().UTC())
			return errors.New("publish retrieval event")
		}
		if err = r.repository.MarkPublished(ctx, record.EventID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Cleanup(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := r.repository.RecoverStaleBatches(ctx, now.Add(-15*time.Minute), now); err != nil {
		return err
	}
	return r.retryPendingVectorCleanup(ctx, now, 64)
}

func loadSecret(ctx context.Context, arn string) (Secret, error) {
	if arn == "" {
		return Secret{}, errors.New("runtime secret ARN is required")
	}
	awsConfiguration, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return Secret{}, errors.New("load AWS configuration")
	}
	response, err := secretsmanager.NewFromConfig(awsConfiguration).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &arn})
	if err != nil || response.SecretString == nil {
		return Secret{}, errors.New("load runtime secret")
	}
	var secret Secret
	if err = json.Unmarshal([]byte(*response.SecretString), &secret); err != nil {
		return Secret{}, errors.New("invalid runtime secret")
	}
	return secret, nil
}

func oneMessage(event RabbitEvent) (string, []byte, error) {
	if len(event.Messages) != 1 {
		return "", nil, errors.New("expected one RabbitMQ queue")
	}
	for queue, messages := range event.Messages {
		if len(messages) != 1 {
			return "", nil, errors.New("expected one RabbitMQ message")
		}
		payload, err := base64.StdEncoding.DecodeString(messages[0].Data)
		return queue, payload, err
	}
	return "", nil, errors.New("missing RabbitMQ message")
}

func eventAttempt(event RabbitEvent) int64 {
	for _, messages := range event.Messages {
		if len(messages) != 1 {
			return 5
		}
		value, found := messages[0].BasicProperties.Headers["x-delivery-count"]
		if !found {
			return 0
		}
		switch count := value.(type) {
		case float64:
			return int64(count)
		case int64:
			return count
		default:
			return 5
		}
	}
	return 5
}

func (r *Runtime) manifestFailureRecorder() manifestFailureRecorder {
	if r.manifestFails != nil {
		return r.manifestFails
	}
	return r.repository
}

func (r *Runtime) batchFailureRecorder() batchFailureRecorder {
	if r.batchFails != nil {
		return r.batchFails
	}
	return r.repository
}

func (r *Runtime) vectorCleanupRepository() vectorCleanupRepository {
	if r.vectorJobs != nil {
		return r.vectorJobs
	}
	return r.repository
}

func (r *Runtime) retryPendingVectorCleanup(ctx context.Context, now time.Time, limit int) error {
	if r.vector == nil {
		return nil
	}
	jobs, err := r.vectorCleanupRepository().PendingVectorCleanup(ctx, limit, now)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err = r.vector.DeactivateJob(ctx, job.JobID); err != nil {
			if retryErr := r.vectorCleanupRepository().RetryVectorCleanup(ctx, job.JobID, now); retryErr != nil {
				return retryErr
			}
			continue
		}
		if err = r.vectorCleanupRepository().CompleteVectorCleanup(ctx, job.JobID); err != nil {
			return err
		}
	}
	return nil
}

func queueContains(value, queue string) bool {
	return value == queue || len(value) > len(queue) && value[:len(queue)] == queue
}

func dial(ctx context.Context, uri string) (*amqp091.Connection, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return amqp091.DialConfig(uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) { return dialer.DialContext(ctx, network, address) }})
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validatePrivateEndpoint(ctx context.Context, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid dependency URL")
	}
	host := parsed.Hostname()
	if address := net.ParseIP(host); address != nil {
		if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
			return nil
		}
		return errors.New("public dependency address")
	}
	resolveContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(resolveContext, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("dependency hostname unavailable")
	}
	for _, address := range addresses {
		if !address.IP.IsPrivate() && !address.IP.IsLoopback() && !address.IP.IsLinkLocalUnicast() {
			return errors.New("public dependency address")
		}
	}
	return nil
}

func validatePrivateBroker(ctx context.Context, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "amqps" || parsed.Host == "" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid broker URL")
	}
	httpsEquivalent := "https://" + parsed.Host
	return validatePrivateEndpoint(ctx, httpsEquivalent)
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
