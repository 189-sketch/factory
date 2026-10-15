package runner

import (
	"encoding/json"
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os"
)

func readStepResult(path string) (*protocol.StepResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workflow result must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return nil, err
	}
	result, err := protocol.ParseStepResult(body)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow result: %w", err)
	}
	return result, nil
}

func factoryPrompt(factory *protocol.FactoryContext, workspace string) string {
	body, _ := json.Marshal(factory)
	return fmt.Sprintf(`

Machinist factory contract (authoritative execution context):
Context reconstructed from GitHub: %s
Disposable task workspace: %s
GitHub issues, comments, pull requests, and commits are the durable workflow record. Local files, logs, and agent session memory are temporary execution scratch, never a source of workflow state. Read the supplied GitHub context and previous reports, not local handoff documents. Preserve the assigned branch and pull request. Never merge. Review must inspect the supplied head commit without changing files or GitHub state.
Return ONLY one JSON object as your final message. Never write a result or handoff file. The object has outcome (complete, blocked, failed), nonempty summary, and factory. Factory has version: 1 and role matching this context. Triage, Spec, and Implement include a nonempty report in factory.report; the foreman posts it to GitHub. Spec commits and pushes its document in the assigned task branch, opens or updates the same draft PR, and returns a canonical GitHub spec_url plus pr_url, branch and head_sha. Its report is a concise handoff, not the full document. Implement reuses that PR, marks it ready after verification, and includes Fixes with the assigned issue in the PR body. Optional fields are spec_url (Spec document GitHub URL), needs_spec (triage only), pr_url, head_sha (full 40-character commit), branch, verdict (review only: accepted, revision_needed, human_decision_needed), and findings (review only). Each finding has severity (p0, p1, p2, p3), file, line, problem, impact, correction. Triage sets needs_spec for consequential decisions. Implement reports canonical GitHub PR URL, branch, and pushed head SHA. Review reports the exact context PR URL and head SHA, verdict, and concrete findings for a nonaccepted verdict. Keep the full JSON below 16 KiB. Report blocked when a prerequisite or decision is missing. Do not create another clone, worktree, branch, competing PR, or workflow state file.
`, body, workspace)
}
