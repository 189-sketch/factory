package controlplane

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestGitHubWebhookSignatureDispatchAndRedelivery(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "webhook-definition", "Review pull request")
	definition, _, err := store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "webhook-definition-inputs", ExpectedGeneration: definition.Generation,
		Name: definition.Name, Prompt: definition.Prompt, Runtime: definition.Runtime,
		AllowedTools: definition.AllowedTools, TimeoutSeconds: definition.TimeoutSeconds,
		Inputs: map[string]string{"audience": "maintainers", "severity": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	registerDefinitionWorker(t, store, "webhook-worker", protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	detail, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "webhook-automation", Title: "Review incoming pull requests",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Parameters: map[string]string{"severity": "critical"},
		Trigger: protocol.AutomationTrigger{
			Type:    protocol.AutomationTriggerGitHubWebhook,
			Actions: []string{"synchronize", "opened"},
		},
	})
	if err != nil || !created {
		t.Fatalf("create webhook Automation: created=%t err=%v", created, err)
	}
	if detail.Automation.DefinitionID != definition.ID ||
		strings.Join(detail.Automation.Trigger.Actions, ",") != "opened,synchronize" {
		t.Fatalf("webhook Automation = %#v", detail.Automation)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "remove-gh-after-webhook-enable", ExpectedGeneration: definition.Generation,
		Name: definition.Name, Prompt: definition.Prompt, Runtime: definition.Runtime,
		AllowedTools: []string{"git"}, TimeoutSeconds: definition.TimeoutSeconds,
		Inputs: definition.Inputs,
	})
	assertErrorCode(t, err, "definition_required_by_webhook")

	secret := []byte("0123456789abcdef0123456789abcdef")
	server := httptest.NewTLSServer(NewGitHubWebhookHandler(store, secret, slog.Default()))
	defer server.Close()
	body := []byte(`{"action":"opened","repository":{"full_name":"owainlewis/factory"},"pull_request":{"number":232,"html_url":"https://github.com/owainlewis/factory/pull/232","title":"Review this change","base":{"ref":"main"},"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)

	invalid := webhookRequest(t, server.URL, body, "delivery-232", "sha256="+strings.Repeat("0", 64))
	response, err := server.Client().Do(invalid)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d", response.StatusCode)
	}
	ignoredBody := []byte(`{"action":"closed","repository":{"full_name":"owainlewis/factory"},"pull_request":{"number":232}}`)
	ignored := webhookRequest(t, server.URL, ignoredBody, "delivery-ignored", signWebhook(secret, ignoredBody))
	response, err = server.Client().Do(ignored)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("ignored signed action status = %d", response.StatusCode)
	}

	signature := signWebhook(secret, body)
	for attempt := 0; attempt < 2; attempt++ {
		request := webhookRequest(t, server.URL, body, "delivery-232", signature)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("delivery attempt %d status = %d body=%s", attempt, response.StatusCode, responseBody)
		}
	}

	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 {
		t.Fatalf("redelivery created %d occurrences", len(current.Occurrences))
	}
	occurrence := current.Occurrences[0]
	if occurrence.DeliveryID != "delivery-232" || occurrence.Event != "pull_request" ||
		occurrence.Action != "opened" || occurrence.PullRequestNumber != 232 || occurrence.RunID == "" {
		t.Fatalf("webhook occurrence = %#v", occurrence)
	}
	run, err := store.Run(context.Background(), occurrence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.SourceKind != "webhook" || len(run.Jobs) != 1 ||
		run.Jobs[0].Job.RepositoryRemoteIdentity != repository.RemoteIdentity {
		t.Fatalf("webhook Run = %#v", run)
	}
	if run.Run.DeliveryID != "delivery-232" || run.Run.Event != "pull_request" ||
		run.Run.Action != "opened" || run.Run.PullRequestNumber != 232 ||
		run.Run.ObservedHeadCommit != strings.Repeat("a", 40) {
		t.Fatalf("webhook Run identity = %#v", run.Run)
	}
	if !strings.Contains(run.Jobs[0].ResolvedPrompt, "Trusted Factory Run parameters:") ||
		!strings.Contains(run.Jobs[0].ResolvedPrompt, `{"audience":"maintainers","severity":"critical"}`) ||
		!strings.Contains(run.Jobs[0].ResolvedPrompt, `"delivery_id":"delivery-232"`) ||
		!strings.Contains(run.Jobs[0].ResolvedPrompt, "Use authenticated gh CLI") {
		t.Fatalf("webhook prompt = %q", run.Jobs[0].ResolvedPrompt)
	}
	var runCount, deliveryCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM github_webhook_deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || deliveryCount != 1 {
		t.Fatalf("redelivery counts: runs=%d deliveries=%d", runCount, deliveryCount)
	}
	if _, err := store.db.Exec(`UPDATE automation_github_webhook_triggers SET automation_id = 'other' WHERE automation_id = ?`, detail.Automation.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("webhook Trigger Automation ID update error = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE automation_github_webhook_occurrences SET automation_id = 'other' WHERE occurrence_id = ?`, occurrence.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("webhook Occurrence Automation ID update error = %v", err)
	}
	var deliveryIndex int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'automation_github_webhook_occurrences_delivery'
	`).Scan(&deliveryIndex); err != nil || deliveryIndex != 1 {
		t.Fatalf("webhook delivery index count=%d err=%v", deliveryIndex, err)
	}
}

func TestGitHubWebhookAutomationRequiresGitHubCLI(t *testing.T) {
	store := newTestStore(t)
	definition, _, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "webhook-without-gh", Name: "Cannot review GitHub",
		Prompt: "Review the pull request.", Runtime: protocol.RuntimeCodex,
		AllowedTools: []string{"git"}, TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/no-gh")
	_, _, err = store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "webhook-no-gh-automation", Title: "Invalid webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	assertErrorCode(t, err, "webhook_gh_required")
}

func TestGitHubWebhookAutomationReservesWorstCaseEnvelope(t *testing.T) {
	store := newTestStore(t)
	definition, created, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "webhook-envelope-definition", Name: "Webhook envelope limit",
		Prompt:  string(bytes.Repeat([]byte("p"), protocol.MaxDefinitionPromptBytes)),
		Runtime: protocol.RuntimeCodex, AllowedTools: []string{"git", "gh"}, TimeoutSeconds: 600,
		Inputs: map[string]string{},
	})
	if err != nil || !created {
		t.Fatalf("create Definition: created=%t err=%v", created, err)
	}
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/webhook-envelope")
	_, created, err = store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "webhook-envelope-automation", Title: "Reject oversized webhook prompt",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if created {
		t.Fatal("webhook Automation with oversized worst-case prompt was created")
	}
	assertErrorCode(t, err, "resolved_prompt_too_large")
}

func TestGitHubWebhookRejectsOversizedPromptMetadata(t *testing.T) {
	store := newTestStore(t)
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "oversized-webhook-metadata", Action: "opened",
		RepositoryIdentity: "github.com/owainlewis/factory",
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 1, URL: "https://github.com/owainlewis/factory/pull/1",
			Title:      strings.Repeat("x", maxGitHubWebhookTitleBytes+1),
			BaseBranch: "main", HeadCommit: strings.Repeat("a", 40),
		},
	}
	_, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("oversized"))
	assertErrorCode(t, err, "invalid_webhook_payload")
}

func TestGitHubWebhookDispatchContinuesAfterOneAutomationFails(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "webhook-fanout-definition", "Review fanout")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/fanout")
	var automationIDs []string
	for index, title := range []string{"Fanout review A", "Fanout review B"} {
		detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
			RequestKey: "webhook-fanout-" + string(rune('a'+index)), Title: title,
			DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
			Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
			t.Fatal(err)
		}
		automationIDs = append(automationIDs, detail.Automation.ID)
	}
	failedAutomationID := automationIDs[0]
	if automationIDs[1] < failedAutomationID {
		failedAutomationID = automationIDs[1]
	}
	if _, err := store.db.Exec(`UPDATE automation_github_webhook_triggers SET parameters_json = '{"undeclared":"value"}' WHERE automation_id = ?`, failedAutomationID); err != nil {
		t.Fatal(err)
	}
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "fanout-delivery", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 10, URL: "https://github.com/owainlewis/fanout/pull/10", Title: "Fan out",
			BaseBranch: "main", HeadCommit: strings.Repeat("b", 40),
		},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("fanout")); err == nil {
		t.Fatal("fanout delivery unexpectedly succeeded despite one broken Automation")
	}
	rows, err := store.db.Query(`
		SELECT occurrence.automation_id, occurrence.state
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ? ORDER BY occurrence.automation_id
	`, delivery.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var automationID, state string
		if err := rows.Scan(&automationID, &state); err != nil {
			t.Fatal(err)
		}
		states[automationID] = state
	}
	if states[failedAutomationID] != "failed" {
		t.Fatalf("failed Automation state = %q", states[failedAutomationID])
	}
	for _, automationID := range automationIDs {
		if automationID != failedAutomationID && states[automationID] != "dispatched" {
			t.Fatalf("later Automation %s state = %q", automationID, states[automationID])
		}
	}
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("fanout successful Run count = %d", runCount)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), failedAutomationID, false, false); err != nil {
		t.Fatal(err)
	}
	if admitted, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("fanout")); err != nil || admitted != 0 {
		t.Fatalf("disabled redelivery admitted=%d err=%v", admitted, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("disabled redelivery Run count=%d err=%v", runCount, err)
	}
	var deliveryState string
	if err := store.db.QueryRow(`SELECT state FROM github_webhook_deliveries WHERE delivery_id = ?`, delivery.DeliveryID).Scan(&deliveryState); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "failed" {
		t.Fatalf("disabled redelivery state = %q", deliveryState)
	}
}

func TestRestoringWebhookDefinitionPreservesDisabledRepositoryBlock(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "restore-webhook-definition", "Restore webhook")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/restore-webhook")
	detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "restore-webhook-automation", Title: "Restore webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	archived, err := store.SetDefinitionArchived(context.Background(), definition.ID, true, definition.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDefinitionArchived(context.Background(), definition.ID, false, archived.Generation); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Automation.Health.Status != "blocked" || current.Automation.Health.Code != "repository_disabled" ||
		!strings.Contains(current.Automation.Health.Message, "Enable the selected repository") {
		t.Fatalf("restored webhook Automation health = %#v", current.Automation)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	_, err = store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false)
	assertErrorCode(t, err, "repository_disabled")
}

func TestEnablingWebhookRepositoryPreservesArchivedDefinitionBlock(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "archived-webhook-definition", "Archived webhook")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/archived-webhook")
	detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "archived-webhook-automation", Title: "Archived webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDefinitionArchived(context.Background(), definition.ID, true, definition.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, true); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Automation.Health.Status != "blocked" || current.Automation.Health.Code != "definition_archived" {
		t.Fatalf("re-enabled webhook Automation health = %#v", current.Automation.Health)
	}
}

func TestGitHubWebhookDeliveryIDRejectsDifferentPayload(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "conflict-webhook-definition", "Review pull request")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	registerDefinitionWorker(t, store, "conflict-webhook-worker", protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	automation, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "conflict-webhook-automation", Title: "Conflict webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), automation.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "same-delivery", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{Number: 1, URL: "https://github.com/owainlewis/factory/pull/1", BaseBranch: "main", HeadCommit: strings.Repeat("a", 40)},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("first")); err != nil {
		t.Fatal(err)
	}
	_, err = store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("different"))
	assertErrorCode(t, err, "delivery_id_conflict")
}

func TestGitHubWebhookDeliveryWithoutMatchIsNotRetained(t *testing.T) {
	store := newTestStore(t)
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "unmatched-delivery", Action: "opened", RepositoryIdentity: "github.com/owainlewis/unmatched",
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 12, URL: "https://github.com/owainlewis/unmatched/pull/12",
			BaseBranch: "main", HeadCommit: strings.Repeat("d", 40),
		},
	}
	if admitted, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("unmatched")); err != nil || admitted != 0 {
		t.Fatalf("unmatched delivery admitted=%d err=%v", admitted, err)
	}
	var deliveries int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM github_webhook_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("unmatched delivery count = %d", deliveries)
	}
}

func TestPendingGitHubWebhookOccurrenceRecoversAfterDispatchInterruption(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "recovery-webhook-definition", "Review pull request")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/recovery")
	registerDefinitionWorker(t, store, "recovery-webhook-worker", protocol.RepositoryRegistration{
		Key: "recovery", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	automation, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "recovery-webhook-automation", Title: "Recovery webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), automation.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.afterGitHubWebhookAdmission = cancel
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "recovery-delivery", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 14, URL: "https://github.com/owainlewis/recovery/pull/14",
			BaseBranch: "main", HeadCommit: strings.Repeat("e", 40),
		},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(ctx, delivery, []byte("recovery")); err == nil {
		t.Fatal("interrupted webhook dispatch unexpectedly succeeded")
	}
	store.afterGitHubWebhookAdmission = nil
	var occurrenceID, state string
	if err := store.db.QueryRow(`
		SELECT occurrence.id, occurrence.state
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ?
	`, delivery.DeliveryID).Scan(&occurrenceID, &state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("interrupted occurrence state = %q", state)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := store.db.QueryRow(`
		SELECT occurrence.state, webhook.run_id
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE occurrence.id = ?
	`, occurrenceID).Scan(&state, &runID); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" || runID == "" {
		t.Fatalf("recovered occurrence state=%q run_id=%q", state, runID)
	}
}

func TestGitHubWebhookBookkeepingRecoversAfterRunCommitAndRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	definition := createTestDefinition(t, store, "committed-run-recovery-definition", "Review pull request")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/committed-run-recovery")
	registerDefinitionWorker(t, store, "committed-run-recovery-worker", protocol.RepositoryRegistration{
		Key: "committed-run-recovery", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	automation, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "committed-run-recovery-automation", Title: "Committed Run recovery",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), automation.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.afterGitHubWebhookRunCreate = cancel
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "committed-run-recovery-delivery", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 15, URL: "https://github.com/owainlewis/committed-run-recovery/pull/15",
			BaseBranch: "main", HeadCommit: strings.Repeat("f", 40),
		},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(ctx, delivery, []byte("committed-run-recovery")); err == nil {
		t.Fatal("dispatch interrupted after Run commit unexpectedly succeeded")
	}
	store.afterGitHubWebhookRunCreate = nil

	var occurrenceID, occurrenceState, runID, deliveryState string
	if err := store.db.QueryRow(`
		SELECT occurrence.id, occurrence.state, webhook.run_id, delivery.state
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		JOIN github_webhook_deliveries delivery ON delivery.delivery_id = webhook.delivery_id
		WHERE webhook.delivery_id = ?
	`, delivery.DeliveryID).Scan(&occurrenceID, &occurrenceState, &runID, &deliveryState); err != nil {
		t.Fatal(err)
	}
	if occurrenceState != "pending" || runID == "" || deliveryState != "accepted" {
		t.Fatalf("interrupted bookkeeping: occurrence=%q run_id=%q delivery=%q", occurrenceState, runID, deliveryState)
	}
	var runCount, dispatchedCount int
	var healthStatus string
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = ?`,
		"automation:"+automation.Automation.ID+":webhook:"+delivery.DeliveryID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT dispatched_count, health_status FROM automations WHERE id = ?`,
		automation.Automation.ID).Scan(&dispatchedCount, &healthStatus); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || dispatchedCount != 0 || healthStatus != "pending" {
		t.Fatalf("interrupted counters: runs=%d dispatched=%d health=%q", runCount, dispatchedCount, healthStatus)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var recoveredRunID string
	if err := store.db.QueryRow(`
		SELECT occurrence.state, webhook.run_id, delivery.state
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		JOIN github_webhook_deliveries delivery ON delivery.delivery_id = webhook.delivery_id
		WHERE occurrence.id = ?
	`, occurrenceID).Scan(&occurrenceState, &recoveredRunID, &deliveryState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = ?`,
		"automation:"+automation.Automation.ID+":webhook:"+delivery.DeliveryID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT dispatched_count, health_status FROM automations WHERE id = ?`,
		automation.Automation.ID).Scan(&dispatchedCount, &healthStatus); err != nil {
		t.Fatal(err)
	}
	if occurrenceState != "dispatched" || recoveredRunID != runID || deliveryState != "completed" ||
		runCount != 1 || dispatchedCount != 1 || healthStatus != "healthy" {
		t.Fatalf("recovered bookkeeping: occurrence=%q run_id=%q delivery=%q runs=%d dispatched=%d health=%q",
			occurrenceState, recoveredRunID, deliveryState, runCount, dispatchedCount, healthStatus)
	}
}

func TestGitHubWebhookFanoutEnforcesOccurrenceLimitAtomically(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "limited-webhook-definition", "Review pull request")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/limited-webhook")
	var automationIDs []string
	for index := 0; index < 2; index++ {
		detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
			RequestKey: "limited-webhook-" + string(rune('a'+index)), Title: "Limited webhook " + string(rune('A'+index)),
			DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
			Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
			t.Fatal(err)
		}
		automationIDs = append(automationIDs, detail.Automation.ID)
	}
	originalLimit := maxGitHubWebhookOccurrences
	maxGitHubWebhookOccurrences = 1
	t.Cleanup(func() { maxGitHubWebhookOccurrences = originalLimit })
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "limited-fanout", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 42, URL: "https://github.com/owainlewis/limited-webhook/pull/42",
			BaseBranch: "main", HeadCommit: strings.Repeat("c", 40),
		},
	}
	_, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("limited fanout"))
	assertErrorCode(t, err, "occurrence_limit_reached")
	var deliveries, occurrences, matched int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM github_webhook_deliveries WHERE delivery_id = ?`, delivery.DeliveryID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrences); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT SUM(matched_count) FROM automations WHERE id IN (?, ?)`, automationIDs[0], automationIDs[1]).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 || occurrences != 0 || matched != 0 {
		t.Fatalf("partial webhook admission: deliveries=%d occurrences=%d matched=%d", deliveries, occurrences, matched)
	}
}

func webhookRequest(t *testing.T, serverURL string, body []byte, deliveryID, signature string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-Hub-Signature-256", signature)
	return request
}

func signWebhook(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
