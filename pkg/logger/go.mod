module github.com/belLena81/raglibrarian/pkg/logger

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/config v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
	go.uber.org/zap v1.27.1
)

require (
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260310060144-d8455ee5d7b2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/config => ../config
	github.com/belLena81/raglibrarian/pkg/domain => ../domain
)
