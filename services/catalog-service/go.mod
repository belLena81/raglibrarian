module github.com/belLena81/raglibrarian/services/catalog-service

go 1.26.5

require (
	github.com/belLena81/raglibrarian/pkg/grpcauth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/internaltls v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/process v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/grpcauth => ../../pkg/grpcauth
	github.com/belLena81/raglibrarian/pkg/internaltls => ../../pkg/internaltls
	github.com/belLena81/raglibrarian/pkg/process => ../../pkg/process
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
)
