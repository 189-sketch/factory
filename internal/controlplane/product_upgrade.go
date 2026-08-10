package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const productUpgradeID = "definitions-runs-v1"

const productUpgradePollingGuidance = "Retired during the V1 upgrade. Replace this poller with a scheduled Definition that uses gh, configure a GitHub webhook, or leave it retired."

func (s *Store) ProductUpgrade(ctx context.Context) (protocol.ProductUpgrade, error) {
	var state string
	var previewJSON, validationJSON []byte
	var createdAt, updatedAt int64
	var completedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT state, preview_json, validation_json, created_at, updated_at, completed_at
		FROM product_model_upgrades WHERE id = ?
	`, productUpgradeID).Scan(&state, &previewJSON, &validationJSON, &createdAt, &updatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.previewProductUpgrade(ctx)
	}
	if err != nil {
		return protocol.ProductUpgrade{}, unavailable(err)
	}
	var result protocol.ProductUpgrade
	if err := json.Unmarshal(previewJSON, &result); err != nil {
		return protocol.ProductUpgrade{}, unavailable(err)
	}
	result.ID = productUpgradeID
	result.State = state
	result.LegacyReadOnly = true
	result.Needed = true
	created, updated := fromMillis(createdAt), fromMillis(updatedAt)
	result.CreatedAt, result.UpdatedAt = &created, &updated
	result.CompletedAt = nullableTime(completedAt)
	if len(validationJSON) != 0 {
		var validation protocol.ProductUpgradeValidation
		if err := json.Unmarshal(validationJSON, &validation); err != nil {
			return protocol.ProductUpgrade{}, unavailable(err)
		}
		result.Validation = &validation
	}
	if state == "draining" {
		active, err := s.countActiveLegacyExecutions(ctx, s.db)
		if err != nil {
			return protocol.ProductUpgrade{}, err
		}
		result.Counts.ActiveExecutions = active
		result.Decisions = []string{"Legacy writes and admissions are frozen. Wait for active legacy executions to finish, or explicitly cancel them."}
		if active == 0 {
			result.Decisions = []string{"All legacy work is terminal. Continue the upgrade to convert compatible schedules."}
		}
	} else if state == "completed" {
		result.Counts.ActiveExecutions = 0
	}
	return result, nil
}

func (s *Store) previewProductUpgrade(ctx context.Context) (protocol.ProductUpgrade, error) {
	result := protocol.ProductUpgrade{
		ID: productUpgradeID, State: "ready", LegacyReadOnly: false,
		Schedules: []protocol.ProductUpgradeSchedule{},
		Polling:   []protocol.ProductUpgradePollingAutomation{},
		Decisions: []string{},
	}
	counts := &result.Counts
	queries := []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM tasks task WHERE NOT EXISTS (SELECT 1 FROM jobs job WHERE job.task_id = task.id)`, &counts.LegacyTasks},
		{`SELECT COUNT(*) FROM attempts attempt JOIN executions execution ON execution.id = attempt.execution_id WHERE NOT EXISTS (SELECT 1 FROM jobs job WHERE job.execution_id = execution.id)`, &counts.LegacyAttempts},
		{`SELECT COUNT(*) FROM workflows`, &counts.LegacyWorkflows},
		{`SELECT COUNT(*) FROM workflow_revisions`, &counts.LegacyWorkflowRevisions},
		{`SELECT COUNT(*) FROM automation_schedule_triggers WHERE definition_id IS NULL`, &counts.CompatibleSchedules},
		{`SELECT COUNT(*) FROM automations WHERE trigger_type IN ('github_issue', 'github_pull_request')`, &counts.GitHubPollingAutomations},
		{`SELECT COUNT(*) FROM automation_occurrences occurrence JOIN automations automation ON automation.id = occurrence.automation_id LEFT JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id WHERE occurrence.state = 'pending' AND (automation.trigger_type IN ('github_issue', 'github_pull_request') OR (automation.trigger_type = 'schedule' AND schedule.definition_id IS NULL))`, &counts.PendingOccurrences},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.value); err != nil {
			return result, unavailable(err)
		}
	}
	active, err := s.countActiveLegacyExecutions(ctx, s.db)
	if err != nil {
		return result, err
	}
	counts.ActiveExecutions = active
	rows, err := s.db.QueryContext(ctx, `
		SELECT automation.id, automation.title, automation.repository_id,
		       schedule.cron, schedule.timezone, schedule.next_due_at, automation.enabled
		FROM automations automation
		JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		WHERE schedule.definition_id IS NULL
		ORDER BY automation.created_at, automation.id
	`)
	if err != nil {
		return result, unavailable(err)
	}
	for rows.Next() {
		var item protocol.ProductUpgradeSchedule
		var nextDue sql.NullInt64
		var enabled int
		if err := rows.Scan(&item.AutomationID, &item.Title, &item.RepositoryID, &item.Cron,
			&item.Timezone, &nextDue, &enabled); err != nil {
			rows.Close()
			return result, unavailable(err)
		}
		item.DefinitionName = productUpgradeDefinitionName(item.Title, item.AutomationID)
		item.NextDueAt = nullableTime(nextDue)
		item.Enabled = enabled != 0
		result.Schedules = append(result.Schedules, item)
	}
	if err := rows.Close(); err != nil {
		return result, unavailable(err)
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT id, title, trigger_type FROM automations
		WHERE trigger_type IN ('github_issue', 'github_pull_request')
		ORDER BY created_at, id
	`)
	if err != nil {
		return result, unavailable(err)
	}
	for rows.Next() {
		var item protocol.ProductUpgradePollingAutomation
		if err := rows.Scan(&item.AutomationID, &item.Title, &item.TriggerType); err != nil {
			rows.Close()
			return result, unavailable(err)
		}
		item.Guidance = productUpgradePollingGuidance
		result.Polling = append(result.Polling, item)
	}
	if err := rows.Close(); err != nil {
		return result, unavailable(err)
	}
	result.Needed = counts.LegacyTasks+counts.LegacyWorkflows+counts.CompatibleSchedules+
		counts.GitHubPollingAutomations > 0
	if counts.ActiveExecutions > 0 {
		result.Decisions = append(result.Decisions, fmt.Sprintf(
			"%d active legacy executions must finish or be explicitly cancelled after legacy writes are frozen.", counts.ActiveExecutions,
		))
	}
	if counts.PendingOccurrences > 0 {
		result.Decisions = append(result.Decisions, fmt.Sprintf(
			"%d pending legacy occurrences will be retained as failed with an explicit upgrade diagnostic.", counts.PendingOccurrences,
		))
	}
	if counts.CompatibleSchedules > 0 {
		result.Decisions = append(result.Decisions,
			"Legacy schedules did not store a runtime. Migrated schedule Definitions use Codex and require Git.")
	}
	if counts.GitHubPollingAutomations > 0 {
		result.Decisions = append(result.Decisions,
			"GitHub polling Automations cannot be converted safely. They remain readable and retired with replacement guidance.")
	}
	return result, nil
}

func (s *Store) ApplyProductUpgrade(ctx context.Context, cancelActive bool) (protocol.ProductUpgrade, error) {
	s.automationDispatchMu.Lock()
	defer s.automationDispatchMu.Unlock()
	current, err := s.ProductUpgrade(ctx)
	if err != nil {
		return protocol.ProductUpgrade{}, err
	}
	if current.State == "completed" || !current.Needed {
		return current, nil
	}
	if current.State == "ready" {
		if err := s.freezeLegacyProduct(ctx, current, cancelActive); err != nil {
			return protocol.ProductUpgrade{}, err
		}
		if s.afterProductUpgradeFreeze != nil {
			s.afterProductUpgradeFreeze()
		}
	} else if cancelActive {
		if err := s.cancelActiveLegacyExecutions(ctx); err != nil {
			return protocol.ProductUpgrade{}, err
		}
	}
	active, err := s.countActiveLegacyExecutions(ctx, s.db)
	if err != nil {
		return protocol.ProductUpgrade{}, err
	}
	if active > 0 {
		return s.ProductUpgrade(ctx)
	}
	if err := s.finishProductUpgrade(ctx); err != nil {
		return protocol.ProductUpgrade{}, err
	}
	return s.ProductUpgrade(ctx)
}

func (s *Store) freezeLegacyProduct(ctx context.Context, preview protocol.ProductUpgrade, cancelActive bool) error {
	encoded, err := json.Marshal(preview)
	if err != nil {
		return unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO product_model_upgrades(
			id, state, cancel_active, preview_json, created_at, updated_at
		) VALUES (?, 'draining', ?, ?, ?, ?)
	`, productUpgradeID, boolInt(cancelActive), encoded, now, now); err != nil {
		return unavailable(err)
	}
	var unfinishedPollerImports int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy_poller_migrations WHERE status != 'finalized'`).Scan(&unfinishedPollerImports); err != nil {
		return unavailable(err)
	}
	if unfinishedPollerImports != 0 {
		return conflict("legacy_poller_migration_active", "finalize the active legacy poller migration before upgrading Factory")
	}
	var definitionCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM definitions`).Scan(&definitionCount); err != nil {
		return unavailable(err)
	}
	if definitionCount+preview.Counts.CompatibleSchedules > protocol.MaxDefinitions {
		return conflict("definition_limit_reached", "archive Definitions or retire legacy schedules before upgrading Factory")
	}
	for _, schedule := range preview.Schedules {
		_, nameKey, err := normalizeDefinitionMutation(normalizedDefinitionMutation{
			Operation: "create", RequestKey: "product-upgrade:schedule:" + schedule.AutomationID,
			Name: schedule.DefinitionName, Prompt: "placeholder", Runtime: protocol.RuntimeCodex,
			AllowedTools: []string{"git"}, TimeoutSeconds: 1,
			Inputs: map[string]string{},
		})
		if err != nil {
			return err
		}
		var conflictingID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM definitions WHERE name_key = ?`, nameKey).Scan(&conflictingID)
		if err == nil {
			return conflict("definition_name_conflict", "a Definition conflicts with migrated schedule "+schedule.AutomationID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET enabled = 0, evaluation_token = NULL, evaluation_started_at = NULL,
		    next_check_at = NULL, health_status = 'disabled',
		    health_code = 'legacy_upgrade',
		    health_message = CASE
		        WHEN trigger_type IN ('github_issue', 'github_pull_request') THEN ?
		        ELSE 'Legacy schedule frozen while Factory completes the V1 upgrade.'
		    END,
		    updated_at = ?
		WHERE trigger_type IN ('github_issue', 'github_pull_request')
		   OR id IN (SELECT automation_id FROM automation_schedule_triggers WHERE definition_id IS NULL)
	`, productUpgradePollingGuidance, now); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation_schedule_triggers SET next_due_at = NULL
		WHERE definition_id IS NULL
	`); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'failed', diagnostic = 'legacy_upgrade_cancelled', retry_at = NULL, updated_at = ?
		WHERE state = 'pending' AND automation_id IN (
			SELECT automation.id FROM automations automation
			LEFT JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
			WHERE automation.trigger_type IN ('github_issue', 'github_pull_request')
			   OR (automation.trigger_type = 'schedule' AND schedule.definition_id IS NULL)
		)
	`, now); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET enabled = 0, updated_at = ? WHERE enabled = 1`, now); err != nil {
		return unavailable(err)
	}
	if cancelActive {
		if err := cancelActiveLegacyExecutionsTx(ctx, tx, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) cancelActiveLegacyExecutions(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if err := cancelActiveLegacyExecutionsTx(ctx, tx, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE product_model_upgrades SET cancel_active = 1, updated_at = ? WHERE id = ?
	`, now, productUpgradeID); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func cancelActiveLegacyExecutionsTx(ctx context.Context, tx *sql.Tx, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions
		SET state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
		    cancellation_requested = CASE WHEN state IN ('preparing', 'running') THEN 1 ELSE cancellation_requested END,
		    updated_at = ?
		WHERE state IN ('queued', 'preparing', 'running')
		  AND NOT EXISTS (SELECT 1 FROM jobs job WHERE job.execution_id = executions.id)
	`, now); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) finishProductUpgrade(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	active, err := s.countActiveLegacyExecutions(ctx, tx)
	if err != nil {
		return err
	}
	if active != 0 {
		return conflict("legacy_work_active", "legacy executions are still active")
	}
	type schedule struct {
		automationID, title, workflowID, repositoryID string
		instructions, contextValue                    string
		timeoutSeconds                                int
		enabled                                       bool
		nextDue                                       *time.Time
	}
	var previewJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT preview_json FROM product_model_upgrades WHERE id = ?`, productUpgradeID).Scan(&previewJSON); err != nil {
		return unavailable(err)
	}
	var preview protocol.ProductUpgrade
	if err := json.Unmarshal(previewJSON, &preview); err != nil {
		return unavailable(err)
	}
	plans := make(map[string]protocol.ProductUpgradeSchedule, len(preview.Schedules))
	for _, plan := range preview.Schedules {
		plans[plan.AutomationID] = plan
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT automation.id, automation.title, workflow.id, automation.repository_id,
		       revision.instructions, automation.context, automation.timeout_seconds
		FROM automations automation
		JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		JOIN workflows workflow ON workflow.id = automation.workflow_id
		JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
		WHERE schedule.definition_id IS NULL
		ORDER BY automation.id
	`)
	if err != nil {
		return unavailable(err)
	}
	var schedules []schedule
	for rows.Next() {
		var value schedule
		if err := rows.Scan(&value.automationID, &value.title, &value.workflowID, &value.repositoryID,
			&value.instructions, &value.contextValue, &value.timeoutSeconds); err != nil {
			rows.Close()
			return unavailable(err)
		}
		plan, exists := plans[value.automationID]
		if !exists {
			rows.Close()
			return unavailable(errors.New("legacy schedule is missing from the frozen upgrade preview"))
		}
		value.enabled, value.nextDue = plan.Enabled, plan.NextDueAt
		schedules = append(schedules, value)
	}
	if err := rows.Close(); err != nil {
		return unavailable(err)
	}
	now := s.now().UnixMilli()
	for _, legacy := range schedules {
		definitionID, err := newID()
		if err != nil {
			return unavailable(err)
		}
		name := productUpgradeDefinitionName(legacy.title, legacy.automationID)
		prompt := "Workflow instructions:\n\n" + legacy.instructions +
			"\n\nAutomation context:\n\n" + legacy.contextValue +
			"\n\nSchedule instruction:\n\nExecute this Definition for each scheduled occurrence. There is no provider item to revalidate."
		value, nameKey, err := normalizeDefinitionMutation(normalizedDefinitionMutation{
			Operation: "create", RequestKey: "product-upgrade:schedule:" + legacy.automationID,
			Name: name, Prompt: prompt, Runtime: protocol.RuntimeCodex,
			AllowedTools: []string{"git"}, TimeoutSeconds: legacy.timeoutSeconds,
			Inputs: map[string]string{},
		})
		if err != nil {
			return err
		}
		var conflictingID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM definitions WHERE name_key = ?`, nameKey).Scan(&conflictingID)
		if err == nil {
			return conflict("definition_name_conflict", "a Definition conflicts with migrated schedule "+legacy.automationID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}
		digest, err := definitionMutationDigest(value)
		if err != nil {
			return unavailable(err)
		}
		tools, _ := json.Marshal(value.AllowedTools)
		inputs, _ := json.Marshal(value.Inputs)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO definitions(
				id, name, name_key, prompt, runtime, allowed_tools, timeout_seconds,
				inputs, generation, archived, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?, ?)
		`, definitionID, value.Name, nameKey, value.Prompt, value.Runtime, tools,
			value.TimeoutSeconds, inputs, now, now); err != nil {
			return unavailable(err)
		}
		if err := insertDefinitionMutation(ctx, tx, value.RequestKey, digest, definitionID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE automations
			SET workflow_id = NULL, context = '', timeout_seconds = 1, version = version + 1,
			    enabled = ?, health_status = CASE WHEN ? = 1 THEN 'pending' ELSE 'disabled' END,
			    health_code = '',
			    health_message = CASE WHEN ? = 1 THEN 'Migrated schedule is ready for its next due instant.' ELSE 'Automation is disabled.' END,
			    updated_at = ?
			WHERE id = ?
		`, boolInt(legacy.enabled), boolInt(legacy.enabled), boolInt(legacy.enabled), now, legacy.automationID); err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation_schedule_triggers
			SET definition_id = ?, parameters_json = '{}', concurrency_limit = 3, next_due_at = ?
			WHERE automation_id = ?
		`, definitionID, nullableTimeMillis(legacy.nextDue), legacy.automationID); err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_schedule_repositories(automation_id, position, repository_id)
			VALUES (?, 0, ?)
		`, legacy.automationID, legacy.repositoryID); err != nil {
			return unavailable(err)
		}
	}
	var validation protocol.ProductUpgradeValidation
	validation.DefinitionsCreated = len(schedules)
	validation.SchedulesConverted = len(schedules)
	validation.ValidatedAt = fromMillis(now)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations WHERE trigger_type IN ('github_issue', 'github_pull_request')`).Scan(&validation.PollingAutomationsRetired); err != nil {
		return unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks task WHERE NOT EXISTS (SELECT 1 FROM jobs job WHERE job.task_id = task.id)`).Scan(&validation.LegacyTasksRetained); err != nil {
		return unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences WHERE workflow_revision_id IS NOT NULL`).Scan(&validation.LegacyOccurrencesRetained); err != nil {
		return unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		WHERE NOT EXISTS (SELECT 1 FROM jobs job WHERE job.execution_id = execution.id)
	`).Scan(&validation.LegacyAttemptsRetained); err != nil {
		return unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE request_key LIKE 'product-upgrade:%'`).Scan(&validation.SyntheticRunsCreated); err != nil {
		return unavailable(err)
	}
	if validation.SyntheticRunsCreated != 0 {
		return unavailable(errors.New("product upgrade created synthetic Runs"))
	}
	encodedValidation, err := json.Marshal(validation)
	if err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE product_model_upgrades
		SET state = 'completed', validation_json = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND state = 'draining'
	`, encodedValidation, now, now, productUpgradeID); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) countActiveLegacyExecutions(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var count int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM executions execution
		WHERE execution.state IN ('queued', 'preparing', 'running')
		  AND NOT EXISTS (SELECT 1 FROM jobs job WHERE job.execution_id = execution.id)
	`).Scan(&count); err != nil {
		return 0, unavailable(err)
	}
	return count, nil
}

func (s *Store) legacyProductReadOnly(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	var state string
	err := queryer.QueryRowContext(ctx, `SELECT state FROM product_model_upgrades WHERE id = ?`, productUpgradeID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	return conflict("legacy_read_only", "legacy Tasks, Runbooks, and polling Automations are read-only after the V1 upgrade starts")
}

func productUpgradeDefinitionName(title, automationID string) string {
	suffix := " · " + automationID
	maxTitle := 100 - utf8.RuneCountInString(suffix)
	if maxTitle < 1 {
		return truncateRunes(automationID, 100)
	}
	return truncateRunes(strings.TrimSpace(title), maxTitle) + suffix
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func nullableTimeMillis(value *time.Time) any {
	if value != nil {
		return value.UnixMilli()
	}
	return nil
}
