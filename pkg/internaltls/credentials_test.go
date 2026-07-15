package internaltls_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
)

func TestCredentialsRequireExplicitFilesAndServerName(t *testing.T) {
	_, err := internaltls.ServerCredentials(internaltls.Files{})
	assert.Error(t, err)
	_, err = internaltls.ClientCredentials(internaltls.Files{CA: "/ca", Certificate: "/cert", Key: "/key"}, "")
	assert.Error(t, err)
}
