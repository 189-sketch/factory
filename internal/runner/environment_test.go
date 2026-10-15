package runner

import (
	"strings"
	"testing"
)

func TestCredentialEnvironmentMerge(t *testing.T) {
	result := mergeEnvironment([]string{"PATH=/bin", "GH_TOKEN=old", "PATH=/usr/bin"}, []string{"GH_TOKEN=first", "GH_TOKEN=app", "GIT_CONFIG_COUNT=1", "PATH=/attacker", "GIT_DIR=/other"})
	joined := strings.Join(result, "\n")
	if strings.Count(joined, "GH_TOKEN=") != 1 || !strings.Contains(joined, "GH_TOKEN=app") || !strings.Contains(joined, "PATH=/usr/bin") || strings.Contains(joined, "attacker") || strings.Contains(joined, "GIT_DIR") {
		t.Fatalf("unexpected environment: %s", joined)
	}
}
