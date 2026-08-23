package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const testCheckpointSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const resumeToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestAnswerRequeuesSameWorkAndStartsFromAuthoritativeCheckpoint(t *testing.T) {
	store, worker, run, work := needsInputWork(t)
	originalPrompt := work.ResolvedPrompt
	if _, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "61000000-0000-4000-8000-000000000000",
		Message:   strings.Repeat("a", protocol.MaxAnswerBytes+1),
	}); !serviceErrorCode(err, "answer_too_large") {
		t.Fatalf("oversized answer error = %v", err)
	}
	unchanged, err := store.Work(context.Background(), work.ID)
	if err != nil || unchanged.State != protocol.WorkNeedsInput || unchanged.Answer != "" {
		t.Fatalf("oversized answer changed Work = %#v, error %v", unchanged, err)
	}
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "61000000-0000-4000-8000-000000000001",
		Message:   "Keep the public behavior backward compatible.",
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: answer.RequestID, Message: answer.Message,
	})
	if err != nil || replayed.ID != answer.ID {
		t.Fatalf("answer replay = %#v, error %v", replayed, err)
	}
	afterAnswer, err := store.Work(context.Background(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAnswer.ID != work.ID || afterAnswer.RunID != run.Run.ID ||
		afterAnswer.ResolvedPrompt != originalPrompt || afterAnswer.State != protocol.WorkQueued ||
		afterAnswer.PendingResumeSHA != testCheckpointSHA || !afterAnswer.CheckpointPublished ||
		afterAnswer.Answer != answer.Message {
		t.Fatalf("answered Work = %#v", afterAnswer)
	}

	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "answer-continuation-claim", LeaseToken: resumeToken,
	})
	if err != nil || claim == nil {
		t.Fatalf("continuation claim = %#v, error %v", claim, err)
	}
	if claim.Session.PendingResumeSHA != testCheckpointSHA || !claim.Session.CheckpointPublished ||
		!strings.Contains(claim.Session.Prompt, "Which behavior should be preserved?") ||
		!strings.Contains(claim.Session.Prompt, answer.Message) ||
		!strings.Contains(claim.Session.Prompt, "Stored updates: 1") ||
		!protocol.AgentUpdatePromptFits(claim.Session.TaskName, claim.Repository.RemoteIdentity, claim.Session.Prompt) {
		t.Fatalf("continuation claim = %#v", claim.Session)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: resumeToken, StartedFromSHA: strings.Repeat("b", 40),
	}); !serviceErrorCode(err, "resume_commit_mismatch") {
		t.Fatalf("wrong resume start error = %v", err)
	}
	stillPending, err := store.Work(context.Background(), work.ID)
	if err != nil || stillPending.PendingResumeSHA != testCheckpointSHA {
		t.Fatalf("mismatched start changed pending resume = %#v, error %v", stillPending, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: resumeToken, StartedFromSHA: testCheckpointSHA,
	}); err != nil {
		t.Fatal(err)
	}
	beforeRuntime, err := store.Work(context.Background(), work.ID)
	if err != nil || beforeRuntime.State != protocol.WorkRunning ||
		beforeRuntime.PendingResumeSHA != testCheckpointSHA {
		t.Fatalf("pre-runtime continuation = %#v, error %v", beforeRuntime, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: resumeToken, StartedFromSHA: testCheckpointSHA, RuntimeStarted: true,
	}); err != nil {
		t.Fatal(err)
	}
	started, err := store.Work(context.Background(), work.ID)
	if err != nil || started.State != protocol.WorkRunning || started.PendingResumeSHA != "" ||
		started.CheckpointSHA != testCheckpointSHA {
		t.Fatalf("started continuation = %#v, error %v", started, err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: resumeToken, State: "failed", Error: "continuation failed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "post-continuation-retry-claim", LeaseToken: strings.Repeat("c", 64),
	})
	if err != nil || retryClaim == nil || retryClaim.Session.PendingResumeSHA != "" ||
		!retryClaim.Session.CheckpointPublished {
		t.Fatalf("post-continuation retry claim = %#v, error %v", retryClaim, err)
	}
}

func TestFailedReadyPostflightRetainsTrustedPRRecoveryEvidence(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "PR recovery", Prompt: "Open a pull request.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "pr-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "pr-recovery-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	const pullRequestURL = "https://github.com/owainlewis/factory/pull/343"
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "63000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Ready.", PullRequestURL: pullRequestURL,
		PullRequestHeadBranch: run.Sessions[0].Target.PublishBranch,
		PullRequestHeadSHA:    testCheckpointSHA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "Delivery evidence could not be revalidated after the agent process stopped.",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Work(context.Background(), run.Sessions[0].ID)
	if err != nil || failed.State != protocol.WorkFailed || failed.PullRequestURL != pullRequestURL ||
		failed.PullRequestHeadSHA != testCheckpointSHA ||
		failed.PullRequestHeadBranch != run.Sessions[0].Target.PublishBranch {
		t.Fatalf("failed ready Work = %#v, error %v", failed, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, failed.ID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "pr-recovery-retry-claim", LeaseToken: resumeToken,
	})
	if err != nil || retryClaim == nil || retryClaim.Session.PullRequestURL != pullRequestURL ||
		retryClaim.Session.PullRequestHeadSHA != testCheckpointSHA {
		t.Fatalf("PR recovery claim = %#v, error %v", retryClaim, err)
	}
}

func TestFailedNeedsInputPostflightRetainsAuthoritativeCheckpoint(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Checkpoint recovery", Prompt: "Ask when blocked.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "checkpoint-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "checkpoint-recovery-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "63500000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateNeedsInput, Message: "Which behavior?",
		CheckpointSHA: testCheckpointSHA, CheckpointPublished: true,
	}); err != nil {
		t.Fatal(err)
	}
	const postflightFailure = "Checkpoint could not be revalidated after the agent process stopped."
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: postflightFailure,
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Work(context.Background(), run.Sessions[0].ID)
	if err != nil || failed.State != protocol.WorkFailed || failed.FailureReason != postflightFailure ||
		failed.CheckpointSHA != testCheckpointSHA || failed.PendingResumeSHA != testCheckpointSHA ||
		!failed.CheckpointPublished || failed.Question != "" {
		t.Fatalf("failed needs-input Work = %#v, error %v", failed, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, failed.ID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "checkpoint-recovery-retry-claim", LeaseToken: resumeToken,
	})
	if err != nil || retryClaim == nil || retryClaim.Session.PendingResumeSHA != testCheckpointSHA ||
		!retryClaim.Session.CheckpointPublished {
		t.Fatalf("checkpoint recovery claim = %#v, error %v", retryClaim, err)
	}
}

func TestPendingResumeSurvivesAnswerCancellationPreparationFailureAndRetry(t *testing.T) {
	store, worker, run, work := needsInputWork(t)
	if _, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "62000000-0000-4000-8000-000000000001", Message: "Use the existing behavior.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelSession(context.Background(), run.Run.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Work(context.Background(), work.ID)
	if err != nil || cancelled.PendingResumeSHA != testCheckpointSHA {
		t.Fatalf("cancelled Work = %#v, error %v", cancelled, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Work(context.Background(), work.ID)
	if err != nil || retried.PendingResumeSHA != testCheckpointSHA || !retried.RetryMayRepeatEffects {
		t.Fatalf("retried Work = %#v, error %v", retried, err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "failed-preparation-claim", LeaseToken: resumeToken,
	})
	if err != nil || claim == nil {
		t.Fatalf("preparation claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: resumeToken, StartedFromSHA: testCheckpointSHA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: resumeToken, State: "failed", Error: "checkpoint ref moved",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Work(context.Background(), work.ID)
	if err != nil || failed.PendingResumeSHA != testCheckpointSHA {
		t.Fatalf("failed preparation Work = %#v, error %v", failed, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	retriedAgain, err := store.Work(context.Background(), work.ID)
	if err != nil || retriedAgain.PendingResumeSHA != testCheckpointSHA || !retriedAgain.RetryMayRepeatEffects {
		t.Fatalf("second retry Work = %#v, error %v", retriedAgain, err)
	}
}

func TestContinuationPromptBoundsHistoryAndKeepsMandatoryRecoveryContext(t *testing.T) {
	state := continuationState{
		title: "Resume", repository: "github.com/owainlewis/factory",
		resolvedPrompt: strings.Repeat("p", 52<<10), publishBranch: "factory/work-resume",
		question: "Which API?", answer: "Keep v1.", checkpointSHA: testCheckpointSHA,
		pullRequestURL:        "https://github.com/owainlewis/factory/pull/343",
		pullRequestHeadBranch: "factory/work-resume", pullRequestHeadSHA: testCheckpointSHA,
		retryMayRepeatEffects: true,
	}
	history := make([]continuationHistory, 0, 220)
	for sequence := 1; sequence <= 219; sequence++ {
		status := protocol.WorkUpdateRunning
		if sequence == 110 || sequence == 219 {
			status = protocol.WorkUpdateFailed
		}
		history = append(history, continuationHistory{
			Sequence: sequence, Status: status, Actor: protocol.WorkUpdateActorAgent,
			Message: strings.Repeat("update ", 180), AcceptedAtMillis: int64(sequence),
		})
	}
	prompt, err := assembleContinuationPrompt(state, history)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		state.resolvedPrompt, state.question, state.answer, state.checkpointSHA,
		state.pullRequestURL, "Stored updates: 219", "omitted updates:", "omitted SHA-256:",
		`"sequence":110`, `"sequence":219`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("continuation prompt missing %q", required)
		}
	}
	if !protocol.AgentUpdatePromptFits(state.title, state.repository, prompt) {
		t.Fatalf("continuation prompt exceeds %d bytes", protocol.MaxAgentPromptBytes)
	}
}

func TestExactReplacementCopiesFrozenExecutionAndReplayWinsBeforeEligibility(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Exact replacement", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "replace-first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "replace-second"})
	if err != nil {
		t.Fatal(err)
	}
	for _, work := range []protocol.Work{first.Sessions[0], second.Sessions[0]} {
		if _, err := store.db.Exec(`
			UPDATE sessions SET state = 'failed', terminal_at = admitted_at,
			       terminal_message = 'failed', execution_provider = 'frozen-provider',
			       execution_model = 'frozen-model', resource_class = 'frozen-resource'
			WHERE id = ?
		`, work.ID); err != nil {
			t.Fatal(err)
		}
	}
	replacement, err := store.ReplaceWork(context.Background(), protocol.ReplaceWorkRequest{
		RequestKey: "exact-replacement", WorkID: first.Sessions[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Result != protocol.AdmissionAdmitted || len(replacement.Run.Sessions) != 1 {
		t.Fatalf("replacement = %#v", replacement)
	}
	replaced := replacement.Run.Sessions[0]
	if replaced.PredecessorWorkID != first.Sessions[0].ID ||
		replaced.Execution.Provider != "frozen-provider" || replaced.Execution.Model != "frozen-model" ||
		replaced.Execution.ResourceClass != "frozen-resource" ||
		replacement.Run.Run.Execution != replaced.Execution ||
		replaced.Target.PublishBranch == first.Sessions[0].Target.PublishBranch {
		t.Fatalf("replaced Work = %#v, Run execution = %#v", replaced, replacement.Run.Run.Execution)
	}
	archived := true
	if _, err := store.SetTaskArchived(context.Background(), task.ID, protocol.SetTaskArchivedRequest{
		Archived: &archived, ExpectedGeneration: task.Generation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 0 WHERE id = ?`, worker.Repositories[0].ID); err != nil {
		t.Fatal(err)
	}
	replay, err := store.ReplaceWork(context.Background(), protocol.ReplaceWorkRequest{
		RequestKey: "exact-replacement", WorkID: first.Sessions[0].ID,
	})
	if err != nil || replay.Result != protocol.AdmissionReplayed || replay.Run.Run.ID != replacement.Run.Run.ID {
		t.Fatalf("replacement replay = %#v, error %v", replay, err)
	}
	if _, err := store.ReplaceWork(context.Background(), protocol.ReplaceWorkRequest{
		RequestKey: "ineligible-replacement", WorkID: second.Sessions[0].ID,
	}); !serviceErrorCode(err, "procedure_not_available") {
		t.Fatalf("ineligible replacement error = %v", err)
	}
}

func needsInputWork(t *testing.T) (*Store, protocol.Worker, protocol.RunDetail, protocol.Work) {
	t.Helper()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Needs input", Prompt: "Implement the requested behavior.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "needs-input-run"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "needs-input-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "60000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateNeedsInput, Message: "Which behavior should be preserved?",
		CheckpointSHA: testCheckpointSHA, CheckpointPublished: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	work, err := store.Work(context.Background(), run.Sessions[0].ID)
	if err != nil || work.State != protocol.WorkNeedsInput || work.PendingResumeSHA != testCheckpointSHA {
		t.Fatalf("needs-input Work = %#v, error %v", work, err)
	}
	return store, worker, run, work
}
