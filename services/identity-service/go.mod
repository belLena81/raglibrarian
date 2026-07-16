// Identity-owned users, credentials, roles, and sessions.

module github.com/belLena81/raglibrarian/services/identity-service

go 1.26.5

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/grpcauth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/internaltls v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/logger v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/process v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/stretchr/testify v1.11.1
	go.uber.org/zap v1.27.1
	golang.org/x/crypto v0.54.0
	golang.org/x/term v0.45.0
	google.golang.org/grpc v1.82.1
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/grpcauth => ../../pkg/grpcauth
	github.com/belLena81/raglibrarian/pkg/internaltls => ../../pkg/internaltls
	github.com/belLena81/raglibrarian/pkg/logger => ../../pkg/logger
	github.com/belLena81/raglibrarian/pkg/process => ../../pkg/process
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
)
