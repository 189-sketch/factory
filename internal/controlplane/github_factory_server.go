package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// GitHubFactoryServer keeps only disposable connections and leases in memory.
// Issues and their trusted journal comments are the authoritative task store.
// Exactly one foreman serves a configured repository; workers may live on any host.
type GitHubFactoryServer struct {
	mu             sync.Mutex
	liveMu         sync.Mutex // Only leases and worker liveness; never held during GitHub requests.
	auth           *Server
	client         GitHubFactoryClient
	definitionPath string
	repositories   map[string]string
	actor          string
	tasks          map[string]*githubTask
	workers        map[string]Worker
	leases         map[string]*githubLease
	loaded         time.Time
	maxConcurrent  int
	handler        http.Handler
}
type githubTask struct {
	issue GitHubIssue
	state *GitHubFactoryState
	repo  string
}
type githubLease struct {
	spec     protocol.RunSpec
	instance string
	last     time.Time
}

func NewGitHubFactoryServer(path, token string, maxConcurrent int) (*GitHubFactoryServer, error) {
	definition, err := config.LoadDefinitions(path)
	if err != nil {
		return nil, err
	}
	return newGitHubFactoryServer(path, token, maxConcurrent, definition.GitHub.Repositories, NewGitHubFactoryClient())
}
func newGitHubFactoryServer(path, token string, maxConcurrent int, repositories map[string]string, client GitHubFactoryClient) (*GitHubFactoryServer, error) {
	csrf, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	s := &GitHubFactoryServer{auth: &Server{workerToken: token, csrfToken: csrf}, client: client, definitionPath: path, repositories: repositories, tasks: map[string]*githubTask{}, workers: map[string]Worker{}, leases: map[string]*githubLease{}, maxConcurrent: maxConcurrent}
	for alias, repo := range repositories {
		if alias == "" {
			return nil, errors.New("repository alias is required")
		}
		if _, err := githubRepositoryPath(repo); err != nil {
			return nil, err
		}
	}
	if len(repositories) > 0 {
		s.actor, err = client.CurrentUser(context.Background())
		if err != nil {
			return nil, fmt.Errorf("GitHub authentication: %w", err)
		}
		if strings.TrimSpace(s.actor) == "" {
			return nil, errors.New("GitHub user identity is missing")
		}
	}
	s.handler, err = s.routes()
	return s, err
}
func (s *GitHubFactoryServer) Handler() http.Handler { return s.handler }
func (s *GitHubFactoryServer) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	server := &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-done
		return err
	}
}
func (s *GitHubFactoryServer) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("PUT /api/v1/factory/agents/{name}", s.auth.authorizeSubmission(s.savePrompt))
	mux.HandleFunc("POST /api/v1/jobs", s.auth.authorizeSubmission(s.submit))
	mux.HandleFunc("POST /api/v1/jobs/{id}/{action}", s.auth.authorizeSubmission(s.action))
	mux.HandleFunc("POST /api/v1/workers/poll", s.auth.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.auth.authorizeWorker(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.auth.authorizeWorker(s.complete))
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return securityHeaders(mux), nil
}
func (s *GitHubFactoryServer) refresh(ctx context.Context, force bool) error {
	if !force && !s.loaded.IsZero() && time.Since(s.loaded) < 15*time.Second {
		return nil
	}
	tasks := map[string]*githubTask{}
	aliases := s.repositoryNames()
	for _, alias := range aliases {
		repo := s.repositories[alias]
		issues, err := s.client.ListTasks(ctx, repo)
		if err != nil {
			return err
		}
		for _, issue := range issues {
			comments, err := s.client.Comments(ctx, repo, issue.Number)
			if err != nil {
				return err
			}
			state, err := ReduceGitHubFactory(issue, comments, s.actor, alias)
			if err != nil {
				return fmt.Errorf("%s issue %d: %w", repo, issue.Number, err)
			}
			task := &githubTask{issue: issue, state: state, repo: repo}
			if state.LastEventID == 0 {
				if issue.State != "open" {
					continue
				}
				if err = s.event(ctx, task, GitHubFactoryEvent{Kind: "created", Role: "triage"}); err != nil {
					return err
				}
			}
			if task.state.Job.State == "running" {
				run := task.state.Job.Runs[len(task.state.Job.Runs)-1]
				if !s.leaseAlive(run.ID) {
					if err = s.event(ctx, task, GitHubFactoryEvent{Kind: "interrupted", RunID: run.ID, Role: task.state.Role}); err != nil {
						return err
					}
					s.deleteLease(run.ID)
				}
			}
			tasks[task.state.Job.ID] = task
		}
	}
	s.tasks = tasks
	for id, lease := range s.liveLeases() {
		task := s.tasks[lease.spec.JobID]
		if task == nil || task.state.Job.State != "running" || !s.leaseAlive(id) {
			s.deleteLease(id)
		}
	}
	s.loaded = time.Now()
	return nil
}
func (s *GitHubFactoryServer) event(ctx context.Context, task *githubTask, event GitHubFactoryEvent) error {
	event.Version = 1
	event.Previous = task.state.LastEventID
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(raw) > 60000 {
		return errors.New("GitHub journal event exceeds 60 KiB; shorten report or prompt")
	}
	// Validate the proposed transition before creating a remote comment.
	synthetic := GitHubComment{ID: event.Previous + 1, Body: GitHubFactoryMarker + string(raw), User: GitHubActor{Login: s.actor}, CreatedAt: time.Now().UTC()}
	comments, err := s.client.Comments(ctx, task.repo, task.issue.Number)
	if err != nil {
		return err
	}
	current, err := ReduceGitHubFactory(task.issue, comments, s.actor, task.state.Job.Repository)
	if err != nil {
		return err
	}
	if current.LastEventID != event.Previous {
		return ErrWorkflowAction
	}
	if _, err = ReduceGitHubFactory(task.issue, append(comments, synthetic), s.actor, task.state.Job.Repository); err != nil {
		return err
	}
	comment, err := s.client.PostComment(ctx, task.repo, task.issue.Number, synthetic.Body)
	if err != nil {
		s.loaded = time.Time{}
		return err
	}
	// Re-read after publication: never execute work whose durable receipt is uncertain.
	comments, err = s.client.Comments(ctx, task.repo, task.issue.Number)
	if err != nil {
		s.loaded = time.Time{}
		return err
	}
	state, err := ReduceGitHubFactory(task.issue, comments, s.actor, task.state.Job.Repository)
	if err != nil {
		s.loaded = time.Time{}
		return err
	}
	if state.LastEventID < comment.ID {
		s.loaded = time.Time{}
		return errors.New("GitHub has not confirmed the journal event")
	}
	task.state = state
	return nil
}
func (s *GitHubFactoryServer) repositoryNames() []string {
	names := make([]string, 0, len(s.repositories))
	for name := range s.repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (s *GitHubFactoryServer) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.refresh(r.Context(), false); err != nil {
		writeError(w, 502, err)
		return
	}
	jobs := []Job{}
	for _, task := range s.tasks {
		jobs = append(jobs, task.state.Job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt) })
	workers := []Worker{}
	for _, worker := range s.liveWorkers() {
		worker.Connected = time.Since(worker.LastSeenAt) < workerAvailabilityWindow
		workers = append(workers, worker)
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs, "workers": workers, "commands": []string{}, "workflows": []string{"factory"}, "repositories": s.repositoryNames(), "triggers": []any{}, "csrf_token": s.auth.csrfToken, "state_backend": "github"})
}
func (s *GitHubFactoryServer) catalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, catalogResponse{Workflows: []string{"factory"}, Commands: []string{}, Repositories: s.repositoryNames()})
}
func (s *GitHubFactoryServer) command(ctx context.Context, repo, role string) (config.ResolvedCommand, error) {
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		return config.ResolvedCommand{}, err
	}
	command, err := definition.ResolveCommand(role)
	if err != nil {
		return command, err
	}
	command.Prompt, command.Hash, err = s.client.AgentPrompt(ctx, repo, role)
	return command, err
}
func (s *GitHubFactoryServer) definitions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := s.definitionRepository(r.URL.Query().Get("repository"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	commands := []commandDefinitionResponse{}
	for _, role := range []string{"triage", "spec", "implement", "review"} {
		command, err := s.command(r.Context(), repo, role)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		commands = append(commands, commandDefinitionResponse{Name: role, Executor: command.Executor, Timeout: command.Timeout.String(), Hash: command.Hash, Prompt: command.Prompt})
	}
	steps := []workflowStepDefinition{}
	for _, role := range []string{"triage", "spec", "implement", "review"} {
		steps = append(steps, workflowStepDefinition{Name: role})
	}
	writeJSON(w, 200, definitionsResponse{Commands: commands, Workflows: map[string][]workflowStepDefinition{"factory": steps}})
}
func (s *GitHubFactoryServer) definitionRepository(alias string) (string, error) {
	if alias != "" {
		repo, ok := s.repositories[alias]
		if !ok {
			return "", errors.New("repository is not configured")
		}
		return repo, nil
	}
	names := s.repositoryNames()
	if len(names) == 0 {
		return "", errors.New("configure a GitHub repository to load agents")
	}
	return s.repositories[names[0]], nil
}
func (s *GitHubFactoryServer) savePrompt(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		Prompt       string `json:"prompt"`
		ExpectedHash string `json:"expected_hash"`
		Repository   string `json:"repository"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	role := r.PathValue("name")
	if role != "triage" && role != "spec" && role != "implement" && role != "review" {
		writeError(w, 404, errors.New("unknown specialist"))
		return
	}
	repo, err := s.definitionRepository(input.Repository)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	hash, err := s.client.SaveAgentPrompt(r.Context(), repo, role, input.Prompt, input.ExpectedHash)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"hash": hash})
}
func (s *GitHubFactoryServer) submit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input submitRequest
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	repo, ok := s.repositories[input.Repository]
	if !ok {
		writeError(w, 400, errors.New("configure the repository's GitHub mapping first"))
		return
	}
	if input.Workflow != "factory" || input.Command != "" {
		writeError(w, 400, errors.New("GitHub factory accepts the factory workflow"))
		return
	}
	body := strings.TrimSpace(input.Spec)
	if body == "" {
		body = strings.TrimSpace(input.Prompt)
	}
	var issue GitHubIssue
	var err error
	if input.SourceURL != "" {
		u, e := url.Parse(input.SourceURL)
		if e != nil {
			writeError(w, 400, errors.New("invalid source URL"))
			return
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || len(parts) != 4 || parts[2] != "issues" || !strings.EqualFold(strings.Join(parts[:2], "/"), repo) {
			writeError(w, 400, errors.New("source must be a GitHub issue in the selected repository"))
			return
		}
		number, e := strconv.Atoi(parts[3])
		if e != nil || number < 1 {
			writeError(w, 400, errors.New("invalid issue number"))
			return
		}
		issue, err = s.client.GetTask(r.Context(), repo, number)
		if err == nil && issue.State != "open" {
			err = errors.New("issue is closed")
		}
		if err == nil {
			err = s.client.AdoptTask(r.Context(), repo, number)
		}
	} else {
		if body == "" {
			writeError(w, 400, errors.New("describe the task or supply an issue"))
			return
		}
		title := strings.TrimSpace(input.Title)
		if title == "" {
			title = strings.SplitN(body, "\n", 2)[0]
			if len(title) > 100 {
				title = title[:100]
			}
		}
		issue, err = s.client.CreateTask(r.Context(), repo, title, body)
	}
	if err != nil {
		writeError(w, 502, err)
		return
	}
	s.loaded = time.Time{}
	comments, err := s.client.Comments(r.Context(), repo, issue.Number)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	state, err := ReduceGitHubFactory(issue, comments, s.actor, input.Repository)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	task := &githubTask{issue: issue, state: state, repo: repo}
	if state.LastEventID == 0 {
		err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "created", Role: "triage"})
	}
	if err != nil {
		writeError(w, 502, err)
		return
	}
	s.tasks[task.state.Job.ID] = task
	writeJSON(w, 201, map[string]string{"id": task.state.Job.ID})
}
func (s *GitHubFactoryServer) action(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		RunID    string `json:"run_id"`
		Feedback string `json:"feedback"`
		Stopped  bool   `json:"previous_process_stopped"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if len(input.Feedback) > 16000 {
		writeError(w, 400, errors.New("feedback exceeds 16 KiB"))
		return
	}
	if err := s.refresh(r.Context(), true); err != nil {
		writeError(w, 502, err)
		return
	}
	task := s.tasks[r.PathValue("id")]
	if task == nil {
		writeError(w, 404, errors.New("task not found"))
		return
	}
	action := r.PathValue("action")
	if action == "complete_merge" {
		f := task.state.Job.Workflow.Factory
		raw, err := readGitHubMerge(r.Context(), f.PRURL)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		if err = validateMergeState(raw, f.HeadSHA); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	switch action {
	case "approve", "retry", "request_changes", "cancel", "complete_merge":
	default:
		writeError(w, 400, errors.New("unknown task action"))
		return
	}
	if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: action, RunID: input.RunID, Role: task.state.Role, Feedback: input.Feedback, Stopped: input.Stopped}); err != nil {
		writeError(w, 409, err)
		return
	}
	if task.state.Job.State != "running" {
		for id, lease := range s.liveLeases() {
			if lease.spec.JobID == task.state.Job.ID {
				s.deleteLease(id)
			}
		}
	}
	writeJSON(w, 200, map[string]string{"status": "accepted"})
}
func (s *GitHubFactoryServer) poll(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var p protocol.PollRequest
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	if p.InstanceID == "" || p.Name == "" {
		writeError(w, 400, errors.New("worker identity required"))
		return
	}
	s.liveMu.Lock()
	s.workers[p.InstanceID] = Worker{InstanceID: p.InstanceID, Name: p.Name, LastSeenAt: time.Now().UTC(), Repositories: p.Repositories}
	s.liveMu.Unlock()
	if err := s.refresh(r.Context(), false); err != nil {
		writeError(w, 502, err)
		return
	}
	for _, lease := range s.liveLeases() {
		if lease.instance == p.InstanceID {
			writeJSON(w, 200, protocol.PollResponse{Run: &lease.spec})
			return
		}
	}
	if !p.Factory || !p.Workflows || !p.Reviews {
		writeJSON(w, 200, protocol.PollResponse{})
		return
	}
	if s.maxConcurrent > 0 && len(s.liveLeases()) >= s.maxConcurrent {
		writeJSON(w, 200, protocol.PollResponse{})
		return
	}
	for id, worker := range s.liveWorkers() {
		if id != p.InstanceID && worker.Name == p.Name && time.Since(worker.LastSeenAt) < workerAvailabilityWindow {
			writeJSON(w, 200, protocol.PollResponse{})
			return
		}
	}
	executors := stringSet(p.Executors)
	repositories := stringSet(p.Repositories)
	readonly := stringSet(p.ReadOnlyExecutors)
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		task := s.tasks[id]
		state := task.state
		if task.issue.State != "open" || state.Job.State != "queued" || !repositories[state.Job.Repository] {
			continue
		}
		role := state.Role
		capable := true
		for _, stage := range []string{"triage", "spec", "implement", "review"} {
			command, err := s.command(r.Context(), task.repo, stage)
			if err != nil {
				writeError(w, 502, err)
				return
			}
			if !executors[command.Executor] || !supportsModel(p.Models, command.Executor, command.Model) || (stage == "review" && !readonly[command.Executor]) {
				capable = false
				break
			}
		}
		if !capable {
			continue
		}
		command, err := s.command(r.Context(), task.repo, role)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		prompt, err := config.RenderTaskTemplate(command.Prompt, *state.Job.Task, "", nil)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		run := state.Job.Runs[len(state.Job.Runs)-1]
		hash := command.Hash
		if hash == "" {
			sum := sha256.Sum256([]byte(command.Prompt))
			hash = "bundled-" + hex.EncodeToString(sum[:])
		}
		if err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "started", RunID: run.ID, Role: role, Prompt: prompt, PromptHash: hash, Executor: command.Executor, Model: command.Model, WorkerName: p.Name}); err != nil {
			writeError(w, 409, err)
			return
		}
		token, err := randomID("lease", 24)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		f := task.state.Job.Workflow.Factory
		var brief strings.Builder
		fmt.Fprintf(&brief, "GitHub task: %s\nRequirements:\n%s\n", task.issue.HTMLURL, task.issue.Body)
		for _, stage := range []string{"triage", "spec", "implement", "review"} {
			if report := task.state.Reports[stage]; report != "" {
				fmt.Fprintf(&brief, "\n%s handoff:\n%s\n", stage, report)
			}
		}
		if task.state.Feedback != "" {
			fmt.Fprintf(&brief, "\nRequested changes or answer:\n%s\n", task.state.Feedback)
		}
		branch := f.Branch
		if branch == "" {
			branch = fmt.Sprintf("codex/issue-%d", task.issue.Number)
		}
		spec := protocol.RunSpec{ID: run.ID, JobID: id, Command: role, CommandHash: hash, Executor: command.Executor, Model: command.Model, Repository: state.Job.Repository, RenderedPrompt: prompt, Workflow: true, TimeoutMillis: command.Timeout.Milliseconds(), LeaseToken: token, Task: state.Job.Task, Factory: &protocol.FactoryContext{Role: role, SpecURL: f.SpecURL, IssueURL: task.issue.HTMLURL, Context: brief.String(), PRURL: f.PRURL, HeadSHA: f.HeadSHA, Branch: branch}}
		s.liveMu.Lock()
		s.leases[run.ID] = &githubLease{spec: spec, instance: p.InstanceID, last: time.Now()}
		s.liveMu.Unlock()
		writeJSON(w, 200, protocol.PollResponse{Run: &spec})
		return
	}
	writeJSON(w, 200, protocol.PollResponse{})
}

// heartbeat deliberately avoids mu: GitHub reads and publication may take longer
// than a heartbeat interval, while worker liveness must remain independent.
func (s *GitHubFactoryServer) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var p protocol.Heartbeat
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	lease := s.leases[r.PathValue("id")]
	if lease == nil || lease.instance != p.InstanceID || lease.spec.LeaseToken != p.LeaseToken {
		writeError(w, 409, ErrLeaseConflict)
		return
	}
	now := time.Now().UTC()
	lease.last = now
	worker := s.workers[p.InstanceID]
	worker.LastSeenAt = now
	s.workers[p.InstanceID] = worker
	w.WriteHeader(204)
}

// Expiration and heartbeat renewal share a lock so a stale snapshot cannot
// discard a heartbeat received while a GitHub request was pending.
func (s *GitHubFactoryServer) leaseAlive(id string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	lease := s.leases[id]
	if lease == nil {
		return false
	}
	if time.Since(lease.last) > leaseDuration {
		delete(s.leases, id)
		return false
	}
	return true
}

func (s *GitHubFactoryServer) liveLease(id string) *githubLease {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if lease := s.leases[id]; lease != nil {
		copy := *lease
		return &copy
	}
	return nil
}
func (s *GitHubFactoryServer) liveLeases() map[string]githubLease {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	result := make(map[string]githubLease, len(s.leases))
	for id, lease := range s.leases {
		result[id] = *lease
	}
	return result
}
func (s *GitHubFactoryServer) liveWorkers() map[string]Worker {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	result := make(map[string]Worker, len(s.workers))
	for id, worker := range s.workers {
		result[id] = worker
	}
	return result
}
func (s *GitHubFactoryServer) deleteLease(id string) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	delete(s.leases, id)
}

func (s *GitHubFactoryServer) complete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxCompletionBytes) {
		return
	}
	var p protocol.Completion
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	id := r.PathValue("id")
	lease := s.liveLease(id)
	if lease == nil || lease.instance != p.InstanceID || lease.spec.LeaseToken != p.LeaseToken {
		writeError(w, 409, ErrLeaseConflict)
		return
	}
	task := s.tasks[lease.spec.JobID]
	if task == nil {
		writeError(w, 404, errors.New("task not found"))
		return
	}
	// Refresh the journal before reconciling a possibly retried completion.
	comments, err := s.client.Comments(r.Context(), task.repo, task.issue.Number)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	current, err := ReduceGitHubFactory(task.issue, comments, s.actor, task.state.Job.Repository)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	task.state = current
	for _, run := range current.Job.Runs {
		if run.ID == id && run.Outcome != "" {
			s.deleteLease(id)
			w.WriteHeader(204)
			return
		}
	}
	var result struct {
		StepResult json.RawMessage `json:"step_result"`
	}
	step := &protocol.StepResult{Outcome: "failed", Summary: p.Error, Factory: &protocol.FactoryResult{Version: 1, Role: lease.spec.Factory.Role}}
	if p.State == "succeeded" {
		if err = json.Unmarshal(p.Result, &result); err == nil {
			step, err = protocol.ParseStepResult(result.StepResult)
		}
		if err != nil {
			writeError(w, 400, err)
			return
		}
	}
	if step.Summary == "" {
		step.Summary = "Execution " + p.State
	}
	if step.Factory == nil || step.Factory.Role != lease.spec.Factory.Role {
		writeError(w, 400, errors.New("factory result role mismatch"))
		return
	}
	if step.Outcome == "complete" && step.Factory.Role != "review" && strings.TrimSpace(step.Factory.Report) == "" {
		writeError(w, 400, errors.New("completed agent must report its GitHub handoff"))
		return
	}
	if err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "completed", RunID: id, Role: lease.spec.Factory.Role, Result: step}); err != nil {
		writeError(w, 409, err)
		return
	}
	s.deleteLease(id)
	w.WriteHeader(204)
}
