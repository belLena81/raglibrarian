module github.com/belLena81/raglibrarian/pkg/tokenverifier

go 1.26

require (
	github.com/google/uuid                          v1.6.0
	github.com/belLena81/raglibrarian/pkg/proto      v0.0.0
	google.golang.org/grpc                          v1.70.0
)

replace github.com/belLena81/raglibrarian/pkg/proto => ../proto
