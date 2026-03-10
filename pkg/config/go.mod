module github.com/belLena81/raglibrarian/pkg/config

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260310060144-d8455ee5d7b2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/belLena81/raglibrarian/pkg/domain => ../domain
