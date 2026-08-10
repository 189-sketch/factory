package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

var maxGitHubWebhookOccurrences = protocol.MaxAutomationOccurrences

const (
	maxGitHubWebhookDeliveryIDBytes = 200
	maxGitHubWebhookURLBytes        = 2048
	maxGitHubWebhookTitleBytes      = 1024
	maxGitHubWebhookBranchBytes     = 255
	maxGitHubWebhookCommitBytes     = 64
)

type GitHubPullRequestWebhook struct {
	DeliveryID         string
	Action             string
	RepositoryIdentity string
	PullRequest        protocol.GitHubPullRequestMatch
}

type webhookOccurrenceAdmission struct {
	ID           string
	AutomationID string
	DefinitionID string
	RepositoryID string
	Parameters   map[string]string
	Snapshot     protocol.DefinitionSnapshot
	Prompt       string
}

type webhookDispatchOutcome struct {
	admission webhookOccurrenceAdmission
	err       error
}

func (s *Store) AcceptGitHubPullRequestWebhook(
	ctx context.Context,
	delivery GitHubPullRequestWebhook,
	payload []byte,
) (int, error) {
	s.automationDispatchMu.Lock()
	defer s.automationDispatchMu.Unlock()
	delivery.DeliveryID = strings.TrimSpace(delivery.DeliveryID)
	delivery.Action = strings.ToLower(strings.TrimSpace(delivery.Action))
	delivery.RepositoryIdentity = strings.ToLower(strings.Trim(strings.TrimSpace(delivery.RepositoryIdentity), "/"))
	if delivery.DeliveryID == "" || len(delivery.DeliveryID) > maxGitHubWebhookDeliveryIDBytes {
		return 0, invalid("invalid_delivery_id", "X-GitHub-Delivery is required and limited to 200 bytes")
	}
	if delivery.Action != "opened" && delivery.Action != "synchronize" {
		return 0, invalid("unsupported_webhook_action", "only pull_request opened and synchronize actions are supported")
	}
	if !strings.HasPrefix(delivery.RepositoryIdentity, "github.com/") || delivery.PullRequest.Number < 1 ||
		delivery.PullRequest.URL == "" || delivery.PullRequest.BaseBranch == "" || delivery.PullRequest.HeadCommit == "" {
		return 0, invalid("invalid_webhook_payload", "the pull_request webhook payload is missing required repository or revision fields")
	}
	if len(delivery.PullRequest.URL) > maxGitHubWebhookURLBytes ||
		len(delivery.PullRequest.Title) > maxGitHubWebhookTitleBytes ||
		len(delivery.PullRequest.BaseBranch) > maxGitHubWebhookBranchBytes ||
		len(delivery.PullRequest.HeadCommit) > maxGitHubWebhookCommitBytes {
		return 0, invalid("invalid_webhook_payload", "the pull_request webhook payload exceeds Factory field limits")
	}
	payloadDigest := sha256.Sum256(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, unavailable(err)
	}
	defer tx.Rollback()
	var storedDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM github_webhook_deliveries WHERE delivery_id = ?`, delivery.DeliveryID).Scan(&storedDigest)
	if err == nil && !bytes.Equal(storedDigest, payloadDigest[:]) {
		return 0, conflict("delivery_id_conflict", "X-GitHub-Delivery was already used with a different payload")
	}
	firstDelivery := errors.Is(err, sql.ErrNoRows)
	if err != nil && !firstDelivery {
		return 0, unavailable(err)
	}
	now := s.now().UnixMilli()
	if firstDelivery {
		type matchingAutomation struct {
			id, title, repositoryID, definitionID string
			version                               int
			parametersJSON                        []byte
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT automation.id, automation.title, automation.version, automation.repository_id,
			       webhook.definition_id, webhook.parameters_json
			FROM automations automation
			JOIN repositories repository ON repository.id = automation.repository_id
			JOIN automation_github_webhook_triggers webhook ON webhook.automation_id = automation.id
			JOIN definitions definition ON definition.id = webhook.definition_id
			WHERE automation.enabled = 1
			  AND repository.enabled = 1
			  AND definition.archived = 0
			  AND lower(repository.remote_identity) = ?
			  AND EXISTS (SELECT 1 FROM json_each(webhook.actions_json) WHERE value = ?)
			ORDER BY automation.id
		`, delivery.RepositoryIdentity, delivery.Action)
		if err != nil {
			return 0, unavailable(err)
		}
		var matches []matchingAutomation
		for rows.Next() {
			var match matchingAutomation
			if err := rows.Scan(&match.id, &match.title, &match.version, &match.repositoryID, &match.definitionID, &match.parametersJSON); err != nil {
				rows.Close()
				return 0, unavailable(err)
			}
			matches = append(matches, match)
		}
		if err := rows.Close(); err != nil {
			return 0, unavailable(err)
		}
		if len(matches) == 0 {
			return 0, nil
		}
		var occurrenceCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
			return 0, unavailable(err)
		}
		if occurrenceCount+len(matches) > maxGitHubWebhookOccurrences {
			return 0, conflict("occurrence_limit_reached", "the durable Occurrence limit has been reached")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO github_webhook_deliveries(
				delivery_id, payload_digest, event, action, repository_identity,
				pull_request_number, pull_request_url, pull_request_title,
				base_branch, head_commit, state, created_at, updated_at
			) VALUES (?, ?, 'pull_request', ?, ?, ?, ?, ?, ?, ?, 'accepted', ?, ?)
		`, delivery.DeliveryID, payloadDigest[:], delivery.Action, delivery.RepositoryIdentity,
			delivery.PullRequest.Number, delivery.PullRequest.URL, delivery.PullRequest.Title,
			delivery.PullRequest.BaseBranch, delivery.PullRequest.HeadCommit, now, now); err != nil {
			return 0, unavailable(err)
		}
		for _, match := range matches {
			definition, err := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, match.definitionID))
			if err != nil {
				return 0, unavailable(err)
			}
			snapshot := definition.Snapshot()
			snapshotJSON, err := json.Marshal(snapshot)
			if err != nil {
				return 0, unavailable(err)
			}
			parameters := make(map[string]string, len(snapshot.Inputs))
			for key, value := range snapshot.Inputs {
				parameters[key] = value
			}
			var overrides map[string]string
			if err := json.Unmarshal(match.parametersJSON, &overrides); err != nil {
				return 0, unavailable(errors.New("stored webhook parameters are invalid"))
			}
			for key, value := range overrides {
				parameters[key] = value
			}
			definitionPrompt, err := protocol.ResolveDefinitionPrompt(snapshot.Prompt, parameters)
			if err != nil {
				return 0, unavailable(err)
			}
			prompt, err := protocol.ResolveGitHubWebhookPrompt(definitionPrompt, delivery.DeliveryID,
				delivery.Action, delivery.RepositoryIdentity, delivery.PullRequest)
			if err != nil {
				return 0, unavailable(err)
			}
			occurrenceID, err := newID()
			if err != nil {
				return 0, unavailable(err)
			}
			runRequestKey := "automation:" + match.id + ":webhook:" + delivery.DeliveryID
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_occurrences(
					id, automation_id, automation_version, automation_title, workflow_revision_id,
					repository_id, repository_identity, context, timeout_seconds, state,
					resolved_prompt, task_request_key, created_at, updated_at
				) VALUES (?, ?, ?, ?, NULL, ?, ?, NULL, NULL, 'pending', ?, ?, ?, ?)
			`, occurrenceID, match.id, match.version, match.title, match.repositoryID,
				delivery.RepositoryIdentity, prompt, runRequestKey, now, now); err != nil {
				return 0, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_github_webhook_occurrences(
					occurrence_id, automation_id, delivery_id, event, action,
					pull_request_number, pull_request_url, pull_request_title, base_branch,
					head_commit, definition_id, definition_snapshot, parameters_json
				) VALUES (?, ?, ?, 'pull_request', ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, occurrenceID, match.id, delivery.DeliveryID, delivery.Action,
				delivery.PullRequest.Number, delivery.PullRequest.URL, delivery.PullRequest.Title,
				delivery.PullRequest.BaseBranch, delivery.PullRequest.HeadCommit,
				match.definitionID, snapshotJSON, match.parametersJSON); err != nil {
				return 0, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE automations SET matched_count = matched_count + 1, last_checked_at = ?,
				    health_status = 'pending', health_code = '', health_message = 'Webhook accepted; starting Run.', updated_at = ?
				WHERE id = ?
			`, now, now, match.id); err != nil {
				return 0, unavailable(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, unavailable(err)
	}
	if s.afterGitHubWebhookAdmission != nil {
		s.afterGitHubWebhookAdmission()
	}
	return s.dispatchGitHubWebhookOccurrences(ctx, delivery.DeliveryID)
}

func (s *Store) dispatchGitHubWebhookOccurrence(ctx context.Context, occurrenceID string) (bool, error) {
	s.automationDispatchMu.Lock()
	defer s.automationDispatchMu.Unlock()
	var deliveryID string
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT webhook.delivery_id, automation.enabled
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		JOIN automations automation ON automation.id = occurrence.automation_id
		WHERE occurrence.id = ? AND occurrence.state = 'pending'
	`, occurrenceID).Scan(&deliveryID, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, unavailable(err)
	}
	if enabled == 0 {
		return true, nil
	}
	_, err = s.dispatchGitHubWebhookOccurrences(ctx, deliveryID)
	return true, err
}

func (s *Store) dispatchGitHubWebhookOccurrences(ctx context.Context, deliveryID string) (int, error) {
	// A failed occurrence becomes pending before another attempt so a process
	// interruption during Run creation can always be recovered on startup.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE automation_occurrences SET state = 'pending', diagnostic = '', updated_at = ?
		WHERE id IN (
			SELECT occurrence.id
			FROM automation_occurrences occurrence
			JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
			JOIN automations automation ON automation.id = occurrence.automation_id
			WHERE webhook.delivery_id = ? AND occurrence.state = 'failed' AND automation.enabled = 1
		)
	`, s.now().UnixMilli(), deliveryID); err != nil {
		return 0, unavailable(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT occurrence.id, occurrence.automation_id, occurrence.repository_id,
		       occurrence.resolved_prompt, occurrence.task_request_key,
		       webhook.definition_snapshot, webhook.parameters_json
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		JOIN automations automation ON automation.id = occurrence.automation_id
		WHERE webhook.delivery_id = ? AND occurrence.state = 'pending'
		  AND automation.enabled = 1
		ORDER BY occurrence.id
	`, deliveryID)
	if err != nil {
		return 0, unavailable(err)
	}
	var admissions []webhookOccurrenceAdmission
	for rows.Next() {
		var value webhookOccurrenceAdmission
		var snapshotJSON, parametersJSON []byte
		var requestKey string
		if err := rows.Scan(&value.ID, &value.AutomationID, &value.RepositoryID, &value.Prompt,
			&requestKey, &snapshotJSON, &parametersJSON); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		if err := json.Unmarshal(snapshotJSON, &value.Snapshot); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		value.DefinitionID = value.Snapshot.ID
		if err := json.Unmarshal(parametersJSON, &value.Parameters); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		admissions = append(admissions, value)
	}
	if err := rows.Close(); err != nil {
		return 0, unavailable(err)
	}
	outcomes := make([]webhookDispatchOutcome, 0, len(admissions))
	var firstDispatchError error
	for _, admission := range admissions {
		_, _, err := s.createWebhookRun(ctx, protocol.CreateRunRequest{
			RequestKey:   "automation:" + admission.AutomationID + ":webhook:" + deliveryID,
			DefinitionID: admission.DefinitionID, RepositoryIDs: []string{admission.RepositoryID},
			ConcurrencyLimit: 1, Parameters: admission.Parameters,
		}, admission.Snapshot, admission.Prompt, admission.ID)
		outcomes = append(outcomes, webhookDispatchOutcome{admission: admission, err: err})
		if err != nil && firstDispatchError == nil {
			firstDispatchError = err
		}
	}
	if s.afterGitHubWebhookRunCreate != nil {
		s.afterGitHubWebhookRunCreate()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	dispatched := 0
	for _, outcome := range outcomes {
		state := "dispatched"
		diagnostic := ""
		if outcome.err != nil {
			state = "failed"
			diagnostic = truncateAutomationDiagnostic(outcome.err.Error())
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE automation_occurrences SET state = ?, diagnostic = ?, updated_at = ?
			WHERE id = ? AND state = 'pending'
		`, state, diagnostic, now, outcome.admission.ID)
		if err != nil {
			return 0, unavailable(err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, unavailable(err)
		}
		if changed != 1 {
			return 0, unavailable(errors.New("webhook occurrence changed during dispatch"))
		}
		if outcome.err != nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE automations SET health_status = 'error', health_code = 'webhook_dispatch_failed',
				    health_message = ?, updated_at = ? WHERE id = ?
			`, diagnostic, now, outcome.admission.AutomationID); err != nil {
				return 0, unavailable(err)
			}
			continue
		}
		dispatched++
		if _, err := tx.ExecContext(ctx, `
			UPDATE automations SET dispatched_count = dispatched_count + 1,
			    health_status = 'healthy', health_code = '',
			    health_message = 'Latest webhook started a Run.', updated_at = ?
			WHERE id = ?
		`, now, outcome.admission.AutomationID); err != nil {
			return 0, unavailable(err)
		}
	}
	var failed, pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE occurrence.state = 'failed'),
			COUNT(*) FILTER (WHERE occurrence.state = 'pending')
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ?
	`, deliveryID).Scan(&failed, &pending); err != nil {
		return 0, unavailable(err)
	}
	state := "completed"
	diagnostic := ""
	if failed > 0 {
		state = "failed"
		diagnostic = "one or more matching Automations could not start a Run"
	} else if pending > 0 {
		state = "accepted"
		diagnostic = "one or more matching Automations are waiting to start a Run"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE github_webhook_deliveries SET state = ?, diagnostic = ?, updated_at = ? WHERE delivery_id = ?`, state, diagnostic, now, deliveryID); err != nil {
		return 0, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, unavailable(err)
	}
	if firstDispatchError != nil {
		return dispatched, firstDispatchError
	}
	return dispatched, nil
}
