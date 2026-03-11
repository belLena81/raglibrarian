// Book/chapter/page CRUD service backed by PostgreSQL.
// Exposes a gRPC server.
// Called by: Query Service (enrichment), Lambdas (status updates), Re-index Scheduler.

module github.com/belLena81/raglibrarian/services/metadata

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/config v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260310060144-d8455ee5d7b2
	github.com/belLena81/raglibrarian/pkg/logger v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.8.0
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/stretchr/testify v1.11.1
	go.uber.org/zap v1.27.1
	google.golang.org/grpc v1.70.0
)

require (
	// integration test only
	github.com/testcontainers/testcontainers-go v0.35.0 // indirect
	github.com/testcontainers/testcontainers-go/modules/rabbitmq v0.35.0
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	dario.cat/mergo v1.0.0 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20210617225240-d185dfc1b5a1 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/cenkalti/backoff/v4 v4.2.1 // indirect
	github.com/containerd/containerd v1.7.18 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/platforms v0.2.1 // indirect
	github.com/cpuguy83/dockercfg v0.3.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/docker v27.1.1+incompatible // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/magiconair/properties v1.8.7 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/patternmatcher v0.6.0 // indirect
	github.com/moby/sys/sequential v0.5.0 // indirect
	github.com/moby/sys/user v0.1.0 // indirect
	github.com/moby/term v0.5.0 // indirect
	github.com/morikuni/aec v1.0.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/shirou/gopsutil/v3 v3.23.12 // indirect
	github.com/shoenig/go-m1cpu v0.1.6 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.3 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.49.0 // indirect
	go.opentelemetry.io/otel v1.32.0 // indirect
	go.opentelemetry.io/otel/metric v1.32.0 // indirect
	go.opentelemetry.io/otel/trace v1.32.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.5 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/config => ../../pkg/config
	github.com/belLena81/raglibrarian/pkg/domain => ../../pkg/domain
	github.com/belLena81/raglibrarian/pkg/logger => ../../pkg/logger
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
	github.com/belLena81/raglibrarian/services/query => ../query
)
