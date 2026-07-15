module github.com/belLena81/raglibrarian/tests/e2e

go 1.26.5

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.79.3
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
)
