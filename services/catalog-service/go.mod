module github.com/belLena81/raglibrarian/services/catalog-service

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.79.3
)

require (
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
