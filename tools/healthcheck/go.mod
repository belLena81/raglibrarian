module github.com/belLena81/raglibrarian/tools/healthcheck

go 1.26.5

require (
	github.com/belLena81/raglibrarian/pkg/internaltls v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.79.3
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace github.com/belLena81/raglibrarian/pkg/internaltls => ../../pkg/internaltls
