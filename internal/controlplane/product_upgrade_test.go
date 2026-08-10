package controlplane

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestProductUpgradeConvertsSchedulesAndRetainsLegacyHistory(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "upgrade-workflow", "Weekly review", "Inspect the repository for bugs.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/upgrade")
	registerDefinitionWorker(t, store, "upgrade-worker", protocol.RepositoryRegistration{
		Key: "upgrade", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	nextDue := time.Date(2026, time.August, 17, 8, 30, 0, 0, time.UTC)
	scheduleID := insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, nextDue, true)
	poller, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "upgrade-poller", Title: "Legacy issue poller",
		WorkflowID: workflow.Workflow.ID, RepositoryID: repository.ID,
		Context: "Triage matching issues.", TimeoutSeconds: 900,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open", PollIntervalSeconds: 60,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "upgrade-history-task", Title: "Historical task",
		WorkerID: "upgrade-worker", RepositoryID: repository.ID,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
		Context:            "Preserve this task.", TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelTask(context.Background(), task.Task.ID); err != nil {
		t.Fatal(err)
	}
	now := store.now().UnixMilli()
	if _, err := store.db.Exec(`
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, completed_at, created_at
		) VALUES ('upgrade-attempt', ?, 'upgrade-worker', 1, 'cancelled', X'01', ?, ?, ?)
	`, task.Execution.ID, now, now, now); err != nil {
		t.Fatal(err)
	}

	preview, err := store.ProductUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Needed || preview.State != "ready" || preview.LegacyReadOnly ||
		preview.Counts.CompatibleSchedules != 1 || preview.Counts.GitHubPollingAutomations != 1 ||
		preview.Counts.LegacyTasks != 1 || preview.Counts.LegacyAttempts != 1 || len(preview.Schedules) != 1 || len(preview.Polling) != 1 {
		t.Fatalf("upgrade preview = %#v", preview)
	}
	if preview.Schedules[0].NextDueAt == nil || !preview.Schedules[0].NextDueAt.Equal(nextDue) || !preview.Schedules[0].Enabled {
		t.Fatalf("schedule preview = %#v", preview.Schedules[0])
	}
	if !strings.Contains(preview.Polling[0].Guidance, "scheduled Definition") ||
		!strings.Contains(preview.Polling[0].Guidance, "GitHub webhook") {
		t.Fatalf("polling guidance = %q", preview.Polling[0].Guidance)
	}

	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || !completed.LegacyReadOnly || completed.Validation == nil ||
		completed.Counts.ActiveExecutions != 0 {
		t.Fatalf("completed upgrade = %#v", completed)
	}
	if completed.Validation.DefinitionsCreated != 1 || completed.Validation.SchedulesConverted != 1 ||
		completed.Validation.PollingAutomationsRetired != 1 || completed.Validation.LegacyTasksRetained != 1 ||
		completed.Validation.LegacyAttemptsRetained != 1 || completed.Validation.SyntheticRunsCreated != 0 {
		t.Fatalf("upgrade validation = %#v", completed.Validation)
	}
	var definitionID, cron, timezone string
	var nextDueMillis int64
	var enabled int
	var workflowID *string
	if err := store.db.QueryRow(`
		SELECT schedule.definition_id, schedule.cron, schedule.timezone, schedule.next_due_at,
		       automation.enabled, automation.workflow_id
		FROM automations automation
		JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		WHERE automation.id = ?
	`, scheduleID).Scan(&definitionID, &cron, &timezone, &nextDueMillis, &enabled, &workflowID); err != nil {
		t.Fatal(err)
	}
	if definitionID == "" || cron != "30 8 * * 1" || timezone != "UTC" ||
		nextDueMillis != nextDue.UnixMilli() || enabled != 1 || workflowID != nil {
		t.Fatalf("converted schedule: definition=%q cron=%q timezone=%q next=%d enabled=%d workflow=%v",
			definitionID, cron, timezone, nextDueMillis, enabled, workflowID)
	}
	definition, err := store.Definition(context.Background(), definitionID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Runtime != protocol.RuntimeCodex || definition.TimeoutSeconds != 1200 ||
		!strings.Contains(definition.Prompt, "Inspect the repository for bugs.") ||
		!strings.Contains(definition.Prompt, "Check every package.") {
		t.Fatalf("migrated Definition = %#v", definition)
	}
	var mappedRepository string
	if err := store.db.QueryRow(`SELECT repository_id FROM automation_schedule_repositories WHERE automation_id = ?`, scheduleID).Scan(&mappedRepository); err != nil {
		t.Fatal(err)
	}
	if mappedRepository != repository.ID {
		t.Fatalf("mapped repository = %q", mappedRepository)
	}
	retired, err := store.Automation(context.Background(), poller.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Automation.Enabled || !strings.Contains(retired.Automation.Health.Message, "scheduled Definition") {
		t.Fatalf("retired poller = %#v", retired.Automation)
	}
	retained, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Task.ID != task.Task.ID || retained.Execution.ID != task.Execution.ID ||
		len(retained.Attempts) != 1 || retained.Attempts[0].ID != "upgrade-attempt" {
		t.Fatalf("retained history = %#v", retained)
	}
	var runs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("synthetic Runs = %d, err=%v", runs, err)
	}
	_, _, err = store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "blocked-workflow", Title: "Blocked", Instructions: "Do not create.",
	})
	assertErrorCode(t, err, "legacy_read_only")
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "blocked-task", Title: "Blocked", Description: "Do not create.",
		WorkerID: "upgrade-worker", RepositoryID: repository.ID, TimeoutSeconds: 600,
	})
	assertErrorCode(t, err, "legacy_read_only")
}

func TestProductUpgradeResumesAfterFreezeAndRestart(t *testing.T) {
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
	workflow := createTestWorkflow(t, store, "restart-upgrade-workflow", "Restart upgrade", "Keep history.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/restart-upgrade")
	registerDefinitionWorker(t, store, "restart-upgrade-worker", protocol.RepositoryRegistration{
		Key: "restart-upgrade", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, nil)
	task, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "restart-upgrade-task", Title: "Active legacy task",
		WorkerID: "restart-upgrade-worker", RepositoryID: repository.ID,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
		Context:            "Cancel explicitly.", TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestTask(t, store, "restart-upgrade-worker", "restart-upgrade-claim", tokenA)
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: tokenA, ProcessIdentity: "restart-upgrade-agent",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.afterProductUpgradeFreeze = cancel
	if _, err := store.ApplyProductUpgrade(ctx, false); err == nil {
		t.Fatal("interrupted product upgrade unexpectedly succeeded")
	}
	store.afterProductUpgradeFreeze = nil
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	draining, err := store.ProductUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if draining.State != "draining" || !draining.LegacyReadOnly || draining.Counts.ActiveExecutions != 1 {
		t.Fatalf("reopened upgrade = %#v", draining)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "restart-blocked-task", Title: "Blocked", Description: "Blocked.",
		WorkerID: "restart-upgrade-worker", RepositoryID: repository.ID, TimeoutSeconds: 600,
	})
	assertErrorCode(t, err, "legacy_read_only")
	draining, err = store.ApplyProductUpgrade(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if draining.State != "draining" || draining.Counts.ActiveExecutions != 1 {
		t.Fatalf("cancellation did not keep upgrade draining = %#v", draining)
	}
	heartbeat, err := store.Heartbeat(context.Background(), claim.Attempt.ID, tokenA)
	if err != nil || !heartbeat.CancellationRequested {
		t.Fatalf("cancellation heartbeat = %#v, err=%v", heartbeat, err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "cancelled", Error: "cancelled for product upgrade",
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || completed.Validation == nil || completed.Validation.LegacyTasksRetained != 1 {
		t.Fatalf("resumed upgrade = %#v", completed)
	}
	retained, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Execution.State != "cancelled" || retained.Task.ID != task.Task.ID ||
		len(retained.Attempts) != 1 || retained.Attempts[0].State != "cancelled" ||
		!strings.Contains(retained.Attempts[0].Error, "product upgrade") {
		t.Fatalf("cancelled retained task = %#v", retained)
	}
}

func TestProductUpgradeHTTPPreviewAndApply(t *testing.T) {
	fixture := newHTTPFixture(t)
	createTestWorkflow(t, fixture.store, "http-upgrade-workflow", "HTTP upgrade", "Retain this runbook.")
	response := fixture.request(http.MethodGet, "/api/v1/migrations/product-model", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	preview := decodeResponse[protocol.ProductUpgrade](t, response)
	if !preview.Needed || preview.State != "ready" || preview.Counts.LegacyWorkflows != 1 {
		t.Fatalf("HTTP preview = %#v", preview)
	}
	if preview.Schedules == nil || preview.Polling == nil || preview.Decisions == nil {
		t.Fatalf("HTTP preview collections must be arrays = %#v", preview)
	}
	response = fixture.request(http.MethodPost, "/api/v1/migrations/product-model/apply", "application/json", "", protocol.ApplyProductUpgradeRequest{})
	requireStatus(t, response, http.StatusOK)
	completed := decodeResponse[protocol.ProductUpgrade](t, response)
	if completed.State != "completed" || !completed.LegacyReadOnly || completed.Validation == nil {
		t.Fatalf("HTTP apply = %#v", completed)
	}
}

func TestProductUpgradeRejectsDefinitionConflictBeforeFreeze(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "conflict-upgrade-workflow", "Conflict upgrade", "Keep this schedule.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/conflict-upgrade")
	scheduleID := insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, time.Now().UTC(), true)
	name := productUpgradeDefinitionName("Legacy weekly review", scheduleID)
	if _, _, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "conflicting-definition", Name: name, Prompt: "Existing prompt.",
		Runtime: protocol.RuntimeCodex, AllowedTools: []string{"git"}, TimeoutSeconds: 600,
		Inputs: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.ApplyProductUpgrade(context.Background(), false)
	assertErrorCode(t, err, "definition_name_conflict")
	preview, err := store.ProductUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if preview.State != "ready" || preview.LegacyReadOnly {
		t.Fatalf("conflicting upgrade changed product state = %#v", preview)
	}
	var enabled int
	if err := store.db.QueryRow(`SELECT enabled FROM automations WHERE id = ?`, scheduleID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("conflicting upgrade froze the legacy schedule: enabled=%d", enabled)
	}
}

func TestProductUpgradeRejectsDefinitionMutationConflictBeforeFreeze(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "mutation-conflict-workflow", "Mutation conflict", "Keep this schedule.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/mutation-conflict")
	scheduleID := insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, time.Now().UTC(), true)
	if _, _, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: productUpgradeDefinitionMutationKey(scheduleID), Name: "Existing mutation", Prompt: "Existing prompt.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 600, Inputs: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.ApplyProductUpgrade(context.Background(), false)
	assertErrorCode(t, err, "definition_request_key_conflict")
	preview, err := store.ProductUpgrade(context.Background())
	if err != nil || preview.State != "ready" || preview.LegacyReadOnly {
		t.Fatalf("conflicting upgrade changed product state = %#v, err=%v", preview, err)
	}
}

func TestProductUpgradeRebuildsFrozenPreviewInsideTransaction(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "fresh-preview-workflow", "Fresh preview", "Keep this schedule.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/fresh-preview")
	scheduleID := insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, time.Now().UTC(), true)
	const renamed = "Renamed at freeze"
	var hookErr error
	store.beforeProductUpgradeFreeze = func() {
		_, hookErr = store.db.Exec(`
			UPDATE automations SET title = ?, title_key = ?, version = version + 1, updated_at = ?
			WHERE id = ?
		`, renamed, normalizeTitleKey(renamed), store.now().UnixMilli(), scheduleID)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	store.beforeProductUpgradeFreeze = nil
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err != nil || completed.State != "completed" || len(completed.Schedules) != 1 {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
	expectedName := productUpgradeDefinitionName(renamed, scheduleID)
	if completed.Schedules[0].Title != renamed || completed.Schedules[0].DefinitionName != expectedName {
		t.Fatalf("frozen preview was stale: %#v", completed.Schedules[0])
	}
	var definitionName string
	if err := store.db.QueryRow(`
		SELECT definition.name
		FROM definitions definition
		JOIN automation_schedule_triggers schedule ON schedule.definition_id = definition.id
		WHERE schedule.automation_id = ?
	`, scheduleID).Scan(&definitionName); err != nil {
		t.Fatal(err)
	}
	if definitionName != expectedName {
		t.Fatalf("migrated Definition name = %q, want %q", definitionName, expectedName)
	}
}

func TestProductUpgradeReservesDefinitionNamesWhileDraining(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "reserved-upgrade-workflow", "Reserved upgrade", "Keep this schedule.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/reserved-upgrade")
	scheduleID := insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, time.Now().UTC(), true)
	reservedName := productUpgradeDefinitionName("Legacy weekly review", scheduleID)

	ctx, cancel := context.WithCancel(context.Background())
	store.afterProductUpgradeFreeze = cancel
	if _, err := store.ApplyProductUpgrade(ctx, false); err == nil {
		t.Fatal("interrupted product upgrade unexpectedly succeeded")
	}
	store.afterProductUpgradeFreeze = nil
	draining, err := store.ProductUpgrade(context.Background())
	if err != nil || draining.State != "draining" {
		t.Fatalf("draining product upgrade = %#v, err=%v", draining, err)
	}
	_, _, err = store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: productUpgradeDefinitionMutationKey(scheduleID), Name: "Reserved request key", Prompt: "Do not collide.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 600, Inputs: map[string]string{},
	})
	assertErrorCode(t, err, "definition_request_key_reserved")

	_, _, err = store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "reserved-name-create", Name: strings.ToUpper(reservedName), Prompt: "Do not collide.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 600, Inputs: map[string]string{},
	})
	assertErrorCode(t, err, "definition_name_reserved")
	definition, created, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "reserved-name-update-base", Name: "Available name", Prompt: "Safe.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 600, Inputs: map[string]string{},
	})
	if err != nil || !created {
		t.Fatalf("create Definition for rename: created=%t err=%v", created, err)
	}
	_, _, err = store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "reserved-name-update", ExpectedGeneration: definition.Generation,
		Name: reservedName, Prompt: definition.Prompt, Runtime: definition.Runtime,
		AllowedTools: definition.AllowedTools, TimeoutSeconds: definition.TimeoutSeconds, Inputs: definition.Inputs,
	})
	assertErrorCode(t, err, "definition_name_reserved")

	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("complete product upgrade = %#v, err=%v", completed, err)
	}
}

func TestProductUpgradeReservesDefinitionCapacityWhileDraining(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "capacity-upgrade-workflow", "Capacity upgrade", "Keep this schedule.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/capacity-upgrade")
	insertLegacyScheduleForUpgrade(t, store, workflow.Workflow.ID, repository.ID, time.Now().UTC(), true)
	now := store.now().UnixMilli()
	if _, err := store.db.Exec(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < ?
		)
		INSERT INTO definitions(
			id, name, name_key, prompt, runtime, allowed_tools, timeout_seconds,
			inputs, generation, archived, created_at, updated_at
		)
		SELECT printf('capacity-%03d', value), printf('Capacity %03d', value),
		       printf('capacity %03d', value), 'Capacity prompt.', 'codex', '[]', 600,
		       '{}', 1, 0, ?, ?
		FROM sequence
	`, protocol.MaxDefinitions-1, now, now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.afterProductUpgradeFreeze = cancel
	if _, err := store.ApplyProductUpgrade(ctx, false); err == nil {
		t.Fatal("interrupted product upgrade unexpectedly succeeded")
	}
	store.afterProductUpgradeFreeze = nil

	_, _, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "capacity-after-freeze", Name: "Would exceed reserved capacity", Prompt: "Do not create.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 600, Inputs: map[string]string{},
	})
	assertErrorCode(t, err, "definition_limit_reached")
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("complete product upgrade = %#v, err=%v", completed, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM definitions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != protocol.MaxDefinitions {
		t.Fatalf("Definition count = %d, want %d", count, protocol.MaxDefinitions)
	}
}

func TestProductUpgradeCountsFinalizedPollerOccurrencesAsRetained(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 91)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{
			QueueID: queueID, WorkflowTitle: "Retained poller workflow", AutomationTitle: "Retained poller Automation",
		}},
	})
	if err != nil || len(imported.Occurrences) != 1 {
		t.Fatalf("import legacy poller = %#v, err=%v", imported, err)
	}
	if _, err := store.SkipLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.Validation == nil || completed.Validation.LegacyOccurrencesRetained != 1 {
		t.Fatalf("completed upgrade validation = %#v, err=%v", completed.Validation, err)
	}
}

func TestProductUpgradeAllowsPreviewedLegacyPollerMigration(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 92)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	createTestWorkflow(t, store, "previewed-poller-workflow", "Previewed poller", "Keep this legacy workflow.")

	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{
		LegacyPollerSelection: migrationSelection(fixture),
	})
	if err != nil || preview.Status != "previewed" {
		t.Fatalf("preview legacy poller = %#v, err=%v", preview, err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
}

func TestProductUpgradeAllowsLegacyTaskRequestReplayAfterFreeze(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "upgrade-replay-workflow", "Upgrade replay", "Keep this task.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/upgrade-replay")
	registerDefinitionWorker(t, store, "upgrade-replay-worker", protocol.RepositoryRegistration{
		Key: "upgrade-replay", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, nil)
	request := protocol.CreateTaskRequest{
		RequestKey: "upgrade-replay-task", Title: "Replay after freeze",
		WorkerID: "upgrade-replay-worker", RepositoryID: repository.ID,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
		Context:            "Return the committed task.", TimeoutSeconds: 600,
	}
	created, wasCreated, err := store.CreateTask(context.Background(), request)
	if err != nil || !wasCreated {
		t.Fatalf("create task: created=%t err=%v", wasCreated, err)
	}
	if _, err := store.CancelTask(context.Background(), created.Task.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
	replayed, wasCreated, err := store.CreateTask(context.Background(), request)
	if err != nil || wasCreated || replayed.Task.ID != created.Task.ID {
		t.Fatalf("task replay after freeze = %#v, created=%t err=%v", replayed, wasCreated, err)
	}
}

func TestProductUpgradeIgnoresUserRunWithProductUpgradeRequestKey(t *testing.T) {
	store := newTestStore(t)
	createTestWorkflow(t, store, "synthetic-run-workflow", "Synthetic Run guard", "Keep this legacy workflow.")
	definition := createTestDefinition(t, store, "synthetic-run-definition", "User Run")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/user-product-upgrade-run")
	run, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "product-upgrade:user-run", DefinitionID: definition.ID,
		RepositoryIDs: []string{repository.ID}, ConcurrencyLimit: 1,
	})
	if err != nil || !created {
		t.Fatalf("create user Run: created=%t err=%v", created, err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" || completed.Validation == nil ||
		completed.Validation.SyntheticRunsCreated != 0 {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
	retained, err := store.Run(context.Background(), run.Run.ID)
	if err != nil || retained.Run.RequestKey != "product-upgrade:user-run" {
		t.Fatalf("retained user Run = %#v, err=%v", retained.Run, err)
	}
}

func TestProductUpgradeIgnoresUserDefinitionMutationWithGeneratedRequestKey(t *testing.T) {
	store := newTestStore(t)
	createTestWorkflow(t, store, "synthetic-mutation-workflow", "Synthetic mutation guard", "Keep this legacy workflow.")
	definition := createTestDefinition(t, store, "synthetic-mutation-definition", "User scheduled Definition")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/user-product-upgrade-mutation")
	automation, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "synthetic-mutation-schedule", Title: "User schedule",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID}, ConcurrencyLimit: 1,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerSchedule, Cron: "0 9 * * *", Timezone: "UTC",
		},
	})
	if err != nil || !created {
		t.Fatalf("create schedule: created=%t err=%v", created, err)
	}
	if _, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "synthetic-mutation-run", DefinitionID: definition.ID,
		RepositoryIDs: []string{repository.ID}, ConcurrencyLimit: 1,
	}); err != nil || !created {
		t.Fatalf("create user Run: created=%t err=%v", created, err)
	}
	if _, changed, err := store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey:         productUpgradeDefinitionMutationKey(automation.Automation.ID),
		ExpectedGeneration: definition.Generation, Name: definition.Name,
		Prompt: definition.Prompt, Runtime: definition.Runtime, AllowedTools: definition.AllowedTools,
		TimeoutSeconds: definition.TimeoutSeconds, Inputs: definition.Inputs,
	}); err != nil || !changed {
		t.Fatalf("update Definition: changed=%t err=%v", changed, err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" || completed.Validation == nil ||
		completed.Validation.SyntheticRunsCreated != 0 {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
}

func TestProductUpgradeAllowsWorkflowMutationReplaysAfterFreeze(t *testing.T) {
	store := newTestStore(t)
	createRequest := protocol.CreateWorkflowRequest{
		RequestKey: "upgrade-workflow-replay", Title: "Upgrade Workflow replay",
		Summary: "Retain idempotency.", Instructions: "Keep this Workflow.",
	}
	created, wasCreated, err := store.CreateWorkflow(context.Background(), createRequest)
	if err != nil || !wasCreated {
		t.Fatalf("create Workflow: created=%t err=%v", wasCreated, err)
	}
	revisionRequest := protocol.CreateWorkflowRevisionRequest{
		RequestKey:         "upgrade-workflow-revision-replay",
		ExpectedRevisionID: created.Workflow.CurrentRevision.ID,
		Title:              "Upgrade Workflow replay", Summary: "Retain revision idempotency.",
		Instructions: "Keep this Workflow revision.",
	}
	revised, wasCreated, err := store.CreateWorkflowRevision(context.Background(), created.Workflow.ID, revisionRequest)
	if err != nil || !wasCreated {
		t.Fatalf("create Workflow revision: created=%t err=%v", wasCreated, err)
	}
	if _, err := store.SetWorkflowEnabled(context.Background(), created.Workflow.ID, false); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
	createReplay, wasCreated, err := store.CreateWorkflow(context.Background(), createRequest)
	if err != nil || wasCreated || createReplay.Workflow.ID != created.Workflow.ID {
		t.Fatalf("Workflow create replay = %#v, created=%t err=%v", createReplay, wasCreated, err)
	}
	revisionReplay, wasCreated, err := store.CreateWorkflowRevision(context.Background(), created.Workflow.ID, revisionRequest)
	if err != nil || wasCreated || revisionReplay.Workflow.CurrentRevision.ID != revised.Workflow.CurrentRevision.ID {
		t.Fatalf("Workflow revision replay = %#v, created=%t err=%v", revisionReplay, wasCreated, err)
	}
	enabledReplay, err := store.SetWorkflowEnabled(context.Background(), created.Workflow.ID, false)
	if err != nil || enabledReplay.Workflow.Enabled {
		t.Fatalf("Workflow enabled-state replay = %#v, err=%v", enabledReplay.Workflow, err)
	}
}

func TestProductUpgradeAllowsLegacyAutomationMutationReplaysAfterFreeze(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "upgrade-automation-workflow", "Upgrade Automation", "Keep this Automation.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/upgrade-automation")
	createRequest := protocol.CreateAutomationRequest{
		RequestKey: "upgrade-automation-replay", Title: "Upgrade Automation replay",
		WorkflowID: workflow.Workflow.ID, RepositoryID: repository.ID,
		Context: "Retain idempotency.", TimeoutSeconds: 600,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open", PollIntervalSeconds: 60,
		},
	}
	created, wasCreated, err := store.CreateAutomation(context.Background(), createRequest)
	if err != nil || !wasCreated {
		t.Fatalf("create Automation: created=%t err=%v", wasCreated, err)
	}
	updateRequest := protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Title: "Updated Automation replay", WorkflowID: workflow.Workflow.ID,
		Context: "Retain updated idempotency.", TimeoutSeconds: 900,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open", PollIntervalSeconds: 120,
		},
	}
	updated, err := store.UpdateAutomation(context.Background(), created.Automation.ID, updateRequest)
	if err != nil || updated.Automation.Version != 2 {
		t.Fatalf("update Automation = %#v, err=%v", updated.Automation, err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), created.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), created.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplyProductUpgrade(context.Background(), false)
	if err != nil || completed.State != "completed" {
		t.Fatalf("product upgrade = %#v, err=%v", completed, err)
	}
	createReplay, wasCreated, err := store.CreateAutomation(context.Background(), createRequest)
	if err != nil || wasCreated || createReplay.Automation.ID != created.Automation.ID {
		t.Fatalf("Automation create replay = %#v, created=%t err=%v", createReplay, wasCreated, err)
	}
	updateReplay, err := store.UpdateAutomation(context.Background(), created.Automation.ID, updateRequest)
	if err != nil || updateReplay.Automation.Version != updated.Automation.Version {
		t.Fatalf("Automation update replay = %#v, err=%v", updateReplay.Automation, err)
	}
	enabledReplay, err := store.SetAutomationEnabled(context.Background(), created.Automation.ID, false, false)
	if err != nil || enabledReplay.Automation.Enabled {
		t.Fatalf("Automation enabled-state replay = %#v, err=%v", enabledReplay.Automation, err)
	}
}

func insertLegacyScheduleForUpgrade(
	t *testing.T,
	store *Store,
	workflowID string,
	repositoryID string,
	nextDue time.Time,
	enabled bool,
) string {
	t.Helper()
	value, titleKey, err := normalizeAutomation(
		"upgrade-schedule", "Legacy weekly review", workflowID, repositoryID,
		"Check every package.", 1200,
		protocol.AutomationTrigger{Type: protocol.AutomationTriggerSchedule, Cron: "30 8 * * 1", Timezone: "UTC"},
		"", nil, nil, 0, true, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := legacyAutomationDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	id := "upgrade-schedule"
	now := store.now().UnixMilli()
	if _, err := store.db.Exec(`
		INSERT INTO automations(
			id, request_key, request_digest, title, title_key, workflow_id,
			repository_id, context, timeout_seconds, enabled, trigger_type,
			health_status, health_message, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'schedule', ?, ?, ?, ?)
	`, id, value.RequestKey, digest, value.Title, titleKey, workflowID, repositoryID,
		value.Context, value.TimeoutSeconds, boolInt(enabled), "healthy", "Waiting for next schedule.", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO automation_schedule_triggers(
			automation_id, cron, timezone, next_due_at, definition_id, parameters_json, concurrency_limit
		) VALUES (?, ?, ?, ?, NULL, '{}', 3)
	`, id, value.Trigger.Cron, value.Trigger.Timezone, nextDue.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return id
}
