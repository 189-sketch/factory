//! Outbound event validation against the committed contract schemas
//! (schema/events/*.json). The schemas are the single source of truth; the
//! SSE fanout validates every event before it leaves the core so a malformed
//! event never reaches the ui (the additive-only contract's guardrail).

use anyhow::{Context, Result};

use crate::storage::EventType;

const ENVELOPE_SCHEMA: &str = include_str!("../../schema/events/envelope.json");
const TASK_STATE_SCHEMA: &str = include_str!("../../schema/events/task-state.json");
const RUN_ACTIVITY_SCHEMA: &str = include_str!("../../schema/events/run-activity.json");
const RUN_OUTCOME_SCHEMA: &str = include_str!("../../schema/events/run-outcome.json");
const REPO_HEALTH_SCHEMA: &str = include_str!("../../schema/events/repo-health.json");

/// Compiles the envelope and per-type payload validators once. Construction
/// fails fast on an uncompilable schema so serve refuses to start rather than
/// stream unvalidated events.
pub struct EventValidator {
    envelope: jsonschema::Validator,
    task_state: jsonschema::Validator,
    run_activity: jsonschema::Validator,
    run_outcome: jsonschema::Validator,
    repo_health: jsonschema::Validator,
}

impl EventValidator {
    pub fn compile() -> Result<Self> {
        Ok(Self {
            envelope: compile_schema(ENVELOPE_SCHEMA, "envelope")?,
            task_state: compile_schema(TASK_STATE_SCHEMA, "task-state")?,
            run_activity: compile_schema(RUN_ACTIVITY_SCHEMA, "run-activity")?,
            run_outcome: compile_schema(RUN_OUTCOME_SCHEMA, "run-outcome")?,
            repo_health: compile_schema(REPO_HEALTH_SCHEMA, "repo-health")?,
        })
    }

    /// Validate an outbound envelope (already wrapped in v/type/seq/ts/
    /// repository/task_id/run_id/payload). Returns the validation error
    /// message when invalid; callers drop the event without breaking the
    /// stream.
    pub fn validate_envelope(&self, envelope: &serde_json::Value) -> Result<(), String> {
        validate(&self.envelope, envelope)?;
        let event_type = envelope
            .get("type")
            .and_then(serde_json::Value::as_str)
            .unwrap_or("");
        let payload = envelope.get("payload").cloned().unwrap_or(serde_json::Value::Null);
        let validator = match event_type {
            "task.state" => &self.task_state,
            "run.activity" => &self.run_activity,
            "run.outcome" => &self.run_outcome,
            "repo.health" => &self.repo_health,
            // Unknown types are tolerated (additive-only): the ui ignores them.
            _ => return Ok(()),
        };
        validate(validator, &payload)
    }

    /// Convenience for tests and the fanout: is this event of the given type
    /// valid against its payload schema?
    pub fn payload_valid(&self, event_type: EventType, payload: &serde_json::Value) -> bool {
        let validator = match event_type {
            EventType::TaskState => &self.task_state,
            EventType::RunActivity => &self.run_activity,
            EventType::RunOutcome => &self.run_outcome,
            EventType::RepoHealth => &self.repo_health,
        };
        validator.is_valid(payload)
    }
}

fn compile_schema(source: &str, name: &str) -> Result<jsonschema::Validator> {
    let schema: serde_json::Value = serde_json::from_str(source)
        .with_context(|| format!("failed to parse the {name} event schema"))?;
    jsonschema::validator_for(&schema)
        .with_context(|| format!("failed to compile the {name} event schema"))
}

fn validate(validator: &jsonschema::Validator, value: &serde_json::Value) -> Result<(), String> {
    if let Err(error) = validator.validate(value) {
        return Err(error.to_string());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn validator() -> EventValidator {
        EventValidator::compile().unwrap()
    }

    #[test]
    fn valid_task_state_envelope_passes() {
        let validator = validator();
        let envelope = serde_json::json!({
            "v": 1,
            "type": "task.state",
            "seq": 3,
            "ts": "2026-07-31T00:00:00Z",
            "repository": "owner/repo",
            "task_id": 1,
            "run_id": 2,
            "payload": {
                "from": "queued",
                "to": "running",
                "workflow": "implement",
                "ticket": {"id": "3", "title": null, "url": null},
            },
        });
        assert!(validator.validate_envelope(&envelope).is_ok());
    }

    #[test]
    fn invalid_payload_is_rejected() {
        let validator = validator();
        // task.state missing the required "to" field.
        let envelope = serde_json::json!({
            "v": 1,
            "type": "task.state",
            "seq": 3,
            "ts": "2026-07-31T00:00:00Z",
            "repository": "owner/repo",
            "task_id": 1,
            "run_id": null,
            "payload": {"from": null, "workflow": "implement", "ticket": {"id": "3"}},
        });
        assert!(validator.validate_envelope(&envelope).is_err());
    }

    #[test]
    fn envelope_missing_required_field_is_rejected() {
        let validator = validator();
        // Missing "seq".
        let envelope = serde_json::json!({
            "v": 1,
            "type": "repo.health",
            "ts": "2026-07-31T00:00:00Z",
            "repository": "owner/repo",
            "payload": {"status": "idle"},
        });
        assert!(validator.validate_envelope(&envelope).is_err());
    }

    #[test]
    fn unknown_event_type_is_tolerated() {
        let validator = validator();
        let envelope = serde_json::json!({
            "v": 1,
            "type": "future.thing",
            "seq": 1,
            "ts": "2026-07-31T00:00:00Z",
            "repository": "owner/repo",
            "payload": {"anything": true},
        });
        assert!(validator.validate_envelope(&envelope).is_ok());
    }

    #[test]
    fn all_four_payload_schemas_accept_valid_payloads() {
        let validator = validator();
        assert!(validator.payload_valid(
            EventType::TaskState,
            &serde_json::json!({"from": null, "to": "queued", "workflow": "w", "ticket": {"id": "1"}}),
        ));
        assert!(validator.payload_valid(
            EventType::RunActivity,
            &serde_json::json!({"sequence": 1, "activity": "x", "truncated": false}),
        ));
        assert!(validator.payload_valid(
            EventType::RunOutcome,
            &serde_json::json!({"outcome": "succeeded", "attempt": 1}),
        ));
        assert!(validator.payload_valid(
            EventType::RepoHealth,
            &serde_json::json!({"status": "idle"}),
        ));
        // And each rejects a malformed payload.
        assert!(!validator.payload_valid(
            EventType::RepoHealth,
            &serde_json::json!({"status": "not-a-status"}),
        ));
        assert!(!validator.payload_valid(
            EventType::RunOutcome,
            &serde_json::json!({"outcome": "running"}),
        ));
    }
}
