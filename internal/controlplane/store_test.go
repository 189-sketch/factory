package controlplane

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/protocol"
)

func TestStoreLeasesPipelineInOrderAndPersistsState(t *testing.T) {
	database := filepath.Join(t.TempDir(), "factory.db")
	store := openTestStore(t, database)
	agents := []config.ResolvedAgent{
		testAgent("plan", "Plan request"),
		testAgent("build", "Build request"),
		testAgent("verify", "Verify request"),
	}
	jobID, err := store.CreateJob(t.Context(), "request", "factory", "pipeline", "code", agents)
	if err != nil {
		t.Fatal(err)
	}

	incompatible, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"other"}, []string{"factory"}))
	if err != nil || incompatible != nil {
		t.Fatalf("incompatible poll = %#v, %v", incompatible, err)
	}
	first, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"factory"}))
	if err != nil || first == nil || first.Agent != "plan" || first.RenderedPrompt != "Plan request" {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	repeated, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"factory"}))
	if err != nil || repeated == nil || repeated.ID != first.ID || repeated.LeaseToken != first.LeaseToken {
		t.Fatalf("repeated lease = %#v, %v", repeated, err)
	}
	if err := store.Complete(t.Context(), first.ID, protocol.Completion{InstanceID: "worker-b", LeaseToken: first.LeaseToken, State: "succeeded"}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("cross-worker completion error = %v", err)
	}
	completion := protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0, Events: "event\n"}
	if err := store.Complete(t.Context(), first.ID, completion); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), first.ID, completion); err != nil {
		t.Fatalf("idempotent completion: %v", err)
	}
	output, err := store.RunOutput(t.Context(), first.ID)
	if err != nil || output.Events != "event\n" {
		t.Fatalf("run output = %#v, %v", output, err)
	}
	second, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"factory"}))
	if err != nil || second == nil || second.Agent != "build" {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: second.LeaseToken, State: "failed", ExitCode: 9, Error: "build failed"}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertFailedPipeline(t, snapshot, jobID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, database)
	snapshot, err = reopened.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertFailedPipeline(t, snapshot, jobID)
}

func TestStoreRejectsContradictoryOutcomes(t *testing.T) {
	for _, test := range []struct {
		state    string
		exitCode int
	}{
		{state: "succeeded", exitCode: 1},
		{state: "failed", exitCode: 0},
		{state: "timed_out", exitCode: 1},
		{state: "cancelled", exitCode: 1},
	} {
		t.Run(test.state, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"))
			if _, err := store.CreateJob(t.Context(), "request", "factory", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
				t.Fatal(err)
			}
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"factory"}))
			if err != nil {
				t.Fatal(err)
			}
			err = store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: test.state, ExitCode: test.exitCode})
			if err == nil {
				t.Fatal("expected contradictory outcome rejection")
			}
			snapshot, snapshotErr := store.Snapshot(t.Context())
			if snapshotErr != nil || snapshot.Jobs[0].Runs[0].State != "running" {
				t.Fatalf("snapshot = %#v, %v", snapshot, snapshotErr)
			}
		})
	}
}

func TestConcurrentPollsLeaseRunOnce(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"))
	if _, err := store.CreateJob(t.Context(), "request", "factory", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run, err := store.Poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"factory"}))
			results <- run
			errorsChannel <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	leased := 0
	for run := range results {
		if run != nil {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased runs = %d, want 1", leased)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testAgent(name, prompt string) config.ResolvedAgent {
	return config.ResolvedAgent{Name: name, Executor: "codex", Prompt: prompt, Timeout: time.Minute, Hash: name + "-hash"}
}

func pollRequest(instance string, executors, repositories []string) protocol.PollRequest {
	return protocol.PollRequest{InstanceID: instance, Name: "test", Executors: executors, Repositories: repositories}
}

func assertFailedPipeline(t *testing.T, snapshot Snapshot, jobID string) {
	t.Helper()
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "failed" {
		t.Fatalf("jobs = %#v", snapshot.Jobs)
	}
	runs := snapshot.Jobs[0].Runs
	if len(runs) != 3 || runs[0].State != "succeeded" || runs[1].State != "failed" || runs[2].State != "skipped" {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[1].Error != "build failed" {
		t.Fatalf("run payloads = %#v", runs)
	}
}
