module github.com/belLena81/raglibrarian/pkg/auth

go 1.26.0

require (
	aidanwoods.dev/go-paseto v1.6.0
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260309122639-6b9c9a70dd75
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.48.0
)

require (
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/belLena81/raglibrarian/pkg/domain => ../../pkg/domain
