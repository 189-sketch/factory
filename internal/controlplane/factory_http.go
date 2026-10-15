package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Query only GitHub state. This never merges a pull request.
func readGitHubMerge(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "gh", "pr", "view", url, "--json", "state,headRefOid,mergeCommit").Output()
}

func validateMergeState(raw []byte, expected string) error {
	var state struct {
		State       string
		HeadRefOid  string
		MergeCommit *struct{ Oid string }
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("invalid GitHub merge response: %w", err)
	}
	if state.State != "MERGED" || state.MergeCommit == nil || state.MergeCommit.Oid == "" {
		return errors.New("GitHub has not confirmed this pull request was merged")
	}
	if state.HeadRefOid != expected {
		return errors.New("pull request head changed since review; review the new head before completing")
	}
	return nil
}
