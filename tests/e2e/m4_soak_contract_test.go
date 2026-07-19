//go:build e2e && m4 && m4_soak

package e2e_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestM4SoakRepeatedIngestion exercises repeated success and permanent-failure
// paths. The bounded iteration count keeps accidental local invocations safe;
// release environments should use at least 100 iterations.
func TestM4SoakRepeatedIngestion(t *testing.T) {
	environment := loadM4Environment(t, false)
	iterations := 100
	if raw := strings.TrimSpace(os.Getenv("M4_E2E_SOAK_ITERATIONS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 10 || parsed > 1000 {
			t.Fatal("M4_E2E_SOAK_ITERATIONS must be an integer between 10 and 1000")
		}
		iterations = parsed
	}
	fixtures := []struct {
		name    string
		stage   string
		failure string
	}{
		{name: "minimal.pdf", stage: "chunks_ready"},
		{name: "multipage.pdf", stage: "chunks_ready"},
		{name: "blank_middle_page.pdf", stage: "chunks_ready"},
		{name: "image_only.pdf", stage: "failed", failure: "no_extractable_text"},
		{name: "malformed.pdf", stage: "failed", failure: "malformed_document"},
	}
	for index := 0; index < iterations; index++ {
		fixture := fixtures[index%len(fixtures)]
		book := uploadM4Fixture(t, environment.edgeURLs[index%len(environment.edgeURLs)], environment.accessToken, environment.fixtureDir, fixture.name)
		book = waitForM4Status(t, environment, environment.edgeURLs[(index+1)%len(environment.edgeURLs)], book.ID, func(current m4Book) bool {
			return current.ProcessingStage == fixture.stage
		})
		assert.Equal(t, fixture.failure, book.ProcessingFailureCategory)
		assert.Positive(t, book.ProcessingVersion)
	}
}
