module github.com/belLena81/raglibrarian/services/catalog-service

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.70.0
)

require (
	golang.org/x/net v0.32.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.5 // indirect
)

replace github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
