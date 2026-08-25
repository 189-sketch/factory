package controlplane

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/protocol"
)

// The runner records up to 64 MiB before base64 encoding. The JSON envelope remains
// below this limit, including per-event and string-escaping overhead.
const maxCompletionBytes = 96 << 20

const workerAvailabilityWindow = 10 * time.Second

//go:embed web/dist/* web/dist/assets/*
var webAssets embed.FS

type Server struct {
	store          *Store
	definitionPath string
	workerToken    string
	csrfToken      string
	handler        http.Handler
}

type statusResponse struct {
	Snapshot
	Agents       []string `json:"agents"`
	Pipelines    []string `json:"pipelines"`
	Repositories []string `json:"repositories"`
	CSRFToken    string   `json:"csrf_token"`
}

type submitRequest struct {
	Prompt     string `json:"prompt"`
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
	Pipeline   string `json:"pipeline"`
}

type agentDefinitionResponse struct {
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Timeout  string `json:"timeout"`
	Hash     string `json:"hash"`
	Prompt   string `json:"prompt"`
}

type pipelineDefinitionResponse struct {
	Name   string   `json:"name"`
	Agents []string `json:"agents"`
}

type definitionsResponse struct {
	Agents    []agentDefinitionResponse    `json:"agents"`
	Pipelines []pipelineDefinitionResponse `json:"pipelines"`
}

func NewServer(store *Store, definitionPath, workerToken string) (*Server, error) {
	csrfToken, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	server := &Server{store: store, definitionPath: definitionPath, workerToken: workerToken, csrfToken: csrfToken}
	server.handler, err = server.routes()
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Serve(ctx context.Context, listen string) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("stop control plane: %w", err)
		}
		return nil
	}
}

func (s *Server) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("POST /api/v1/jobs", s.submit)
	mux.HandleFunc("POST /api/v1/workers/poll", s.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.authorizeWorker(s.complete))
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return securityHeaders(mux), nil
}

func (s *Server) definitions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	agents := make([]agentDefinitionResponse, 0, len(definition.Agents))
	for _, name := range mapKeys(definition.Agents) {
		agent, err := config.LoadAgent(s.definitionPath, name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		agents = append(agents, agentDefinitionResponse{Name: agent.Name, Executor: agent.Executor, Timeout: agent.Timeout.String(), Hash: agent.Hash, Prompt: agent.Prompt})
	}
	pipelines := make([]pipelineDefinitionResponse, 0, len(definition.Pipelines))
	for _, name := range mapKeys(definition.Pipelines) {
		resolved, err := config.LoadPipeline(s.definitionPath, name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		agentNames := make([]string, 0, len(resolved))
		for _, agent := range resolved {
			agentNames = append(agentNames, agent.Name)
		}
		pipelines = append(pipelines, pipelineDefinitionResponse{Name: name, Agents: agentNames})
	}
	writeJSON(response, http.StatusOK, definitionsResponse{Agents: agents, Pipelines: pipelines})
}

func (s *Server) status(response http.ResponseWriter, request *http.Request) {
	snapshot, err := s.store.Snapshot(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	repositories, err := s.store.AvailableRepositories(request.Context(), time.Now().Add(-workerAvailabilityWindow))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, statusResponse{
		Snapshot:     snapshot,
		Agents:       mapKeys(definition.Agents),
		Pipelines:    mapKeys(definition.Pipelines),
		Repositories: repositories,
		CSRFToken:    s.csrfToken,
	})
}

func (s *Server) submit(response http.ResponseWriter, request *http.Request) {
	if !s.validBrowserRequest(request) {
		writeError(response, http.StatusForbidden, errors.New("invalid submission origin or CSRF token"))
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	var input submitRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.Repository) == "" {
		writeError(response, http.StatusBadRequest, errors.New("repository is required"))
		return
	}
	if (input.Agent == "") == (input.Pipeline == "") {
		writeError(response, http.StatusBadRequest, errors.New("exactly one agent or pipeline is required"))
		return
	}
	var (
		agents []config.ResolvedAgent
		kind   string
		name   string
		err    error
	)
	if input.Agent != "" {
		kind, name = "agent", input.Agent
		var agent config.ResolvedAgent
		agent, err = config.LoadAgent(s.definitionPath, input.Agent)
		agents = []config.ResolvedAgent{agent}
	} else {
		kind, name = "pipeline", input.Pipeline
		agents, err = config.LoadPipeline(s.definitionPath, input.Pipeline)
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	for index := range agents {
		agents[index], err = config.RenderPrompt(agents[index], input.Prompt)
		if err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
	}
	jobID, err := s.store.CreateJob(request.Context(), input.Prompt, input.Repository, kind, name, agents)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"id": jobID})
}

func (s *Server) poll(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	var input protocol.PollRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance_id and name are required"))
		return
	}
	run, err := s.store.Poll(request.Context(), input)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, protocol.PollResponse{Run: run})
}

func (s *Server) complete(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maxCompletionBytes)
	var input protocol.Completion
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Complete(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeWorker(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if provided == request.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(provided), []byte(s.workerToken)) != 1 {
			response.Header().Set("WWW-Authenticate", "Bearer")
			writeError(response, http.StatusUnauthorized, errors.New("invalid worker token"))
			return
		}
		next(response, request)
	}
}

func (s *Server) validBrowserRequest(request *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Factory-CSRF")), []byte(s.csrfToken)) != 1 {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != "http" || !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	hostname := origin.Hostname()
	return hostname == "localhost" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback()
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must use a loopback host", listen)
	}
	return nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON: %w", err)
		}
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
