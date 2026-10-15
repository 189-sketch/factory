package runner

import (
	"encoding/json"
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os"
	"strings"
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
	if factory.Recipe == "single" {
		body, _ := json.Marshal(factory)
		return fmt.Sprintf(`
Machinist single-agent task contract. You are the only agent for this task; no separate triage, planning, or review stages follow. Complete the operator's request using the assigned workspace and branch. Operational requests can finish with a concise result. Requested code, reports, documents, and other files must be committed and delivered through the assigned PR; do not leave a requested deliverable only in scratch. Reuse the existing PR if supplied. Never merge. Never post internal prompts, journals, or handoffs to issues. Create or edit a GitHub issue only when the operator explicitly requests that action, never solely to track this local job.
Return only one JSON object: {"outcome":"complete|blocked|failed","summary":"...","factory":{"version":1,"role":"implement","report":"..."}}. Include canonical pr_url, full head_sha and branch when delivering repository changes. No PR is required for an operational task that produces no file or commit. Report blocked when a prerequisite is missing. Context: %s
Disposable workspace: %s
`, body, workspace)
	}
	if factory.Recipe == "answer" {
		return fmt.Sprintf(`
Machinist Answer contract: this job is read-only. Answer the request using the supplied context. Never change files, commit, push, open an issue or PR, or produce a requested file. If a deliverable is needed, return blocked and explain that a delivery job is required. Return ONLY JSON: {"outcome":"complete|blocked|failed","summary":"...","factory":{"version":1,"role":"triage","report":"...","resolved":true}}. Set resolved=true only for a complete answer. Do not request a spec or report delivery metadata. Untrusted task context follows:
%s
`, factory.Context)
	}
	body, _ := json.Marshal(factory)
	if factory.Role == "triage" {
		return fmt.Sprintf(`
Machinist triage contract. Inspect the task without modifying files, committing, or writing to GitHub. Classify the request, suggest exact existing label names from the supplied catalog, and decide whether consequential decisions require a specification. Requested code or file changes must continue to Build; never claim to deliver them during Triage. Return ONLY JSON with outcome, summary, and factory:{version:1,role:"triage",report:"...",needs_spec:true|false,labels:[]}. Operational questions belong in Single agent mode. Context: %s
`, body)
	}
	return fmt.Sprintf(`

Machinist factory contract (authoritative execution context):
Task context: %s
Disposable task workspace: %s
Machinist stores task execution state, prompt snapshots, and internal handoffs locally. GitHub holds published specifications, code, and pull requests; a source issue is optional. Never post prompts, execution events, or internal handoffs to issues, and never create an issue for a local request. Read the supplied task context and previous reports. Local worktrees and agent session memory are disposable scratch. Preserve the assigned branch and pull request. Never merge. Review must inspect the supplied head commit without changing files or GitHub state.
Return ONLY one JSON object as your final message. Never write an internal result or handoff file. Requested deliverables are different: reports, documents, HTML pages, and other requested files must be tracked on the assigned branch and delivered through the task PR, even when no production code changes. Follow repository document conventions, otherwise use docs/reports/<task-name>.<extension>. Do not deliver only a scratch file or task artifact. This rule takes precedence over an earlier handoff proposing artifact-only delivery. The object has outcome (complete, blocked, failed), nonempty summary, and factory. Factory has version: 1 and role matching this context. Triage, Spec, and Implement include a nonempty report in factory.report; the foreman stores it as an internal handoff. Spec commits and pushes its document in the assigned task branch, opens or updates the same draft PR, and returns a canonical GitHub spec_url plus pr_url, branch and head_sha. Its report is a concise handoff, not the full document. Implement reuses that PR, marks it ready after verification, and includes Fixes only when an existing GitHub issue is assigned. Optional fields are resolved (triage only: true for an answered plain question or health check with no requested file or code change; never skip a requested deliverable, and do not request a spec when resolved), spec_url (Spec document GitHub URL), needs_spec (triage only), pr_url, head_sha (full 40-character commit), branch, verdict (review only: accepted, revision_needed, human_decision_needed), and findings (review only). Each finding has severity (p0, p1, p2, p3), file, line, problem, impact, correction. Triage sets needs_spec for consequential decisions. Implement reports canonical GitHub PR URL, branch, and pushed head SHA. Review reports the exact context PR URL and head SHA, verdict, and concrete findings for a nonaccepted verdict. Keep the full JSON below 16 KiB. Report blocked when a prerequisite or decision is missing. Do not create another clone, worktree, branch, competing PR, or workflow state file.
`, body, workspace)
}

// Native CLIs sometimes wrap a valid final object in one Markdown code block.
// Normalize that presentation only; never search prose for an embedded object.
func parseFactoryMessage(message string) (*protocol.StepResult, error) {
	if len(message) > 16384 {
		return nil, fmt.Errorf("step result exceeds 16 KiB")
	}
	text := strings.TrimSpace(message)
	if strings.HasPrefix(text, "```json\n") {
		text = strings.TrimPrefix(text, "```json\n")
	} else if strings.HasPrefix(text, "```\n") {
		text = strings.TrimPrefix(text, "```\n")
	} else {
		return protocol.ParseStepResult([]byte(text))
	}
	if !strings.HasSuffix(text, "\n```") {
		return nil, fmt.Errorf("final JSON code block is not closed")
	}
	return protocol.ParseStepResult([]byte(strings.TrimSuffix(text, "\n```")))
}
