package process_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/pkg/process"
)

func TestDropPrivilegesRejectsInvalidIdentityBeforeSyscalls(t *testing.T) {
	assert.Error(t, process.DropPrivileges(process.Identity{}))
}
