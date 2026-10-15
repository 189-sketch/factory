package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGitHubConnection(t *testing.T, h http.HandlerFunc) *GitHubConnection {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := NewGitHubConnection(t.TempDir(), "Iv1.example", "https://github.com/apps/machinist/installations/new")
	if err != nil {
		t.Fatal(err)
	}
	c.apiURL = s.URL
	c.oauthURL = s.URL
	return c
}
func TestGitHubConnectionDeviceFlow(t *testing.T) {
	ctx := context.Background()
	poll := 0
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			w.Write([]byte(`{"device_code":"secret-device","user_code":"ABCD","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`))
		case "/login/oauth/access_token":
			poll++
			if poll == 1 {
				w.Write([]byte(`{"error":"slow_down"}`))
			} else {
				w.Write([]byte(`{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600}`))
			}
		case "/user":
			if r.Header.Get("Authorization") != "Bearer secret-access" {
				t.Error("missing token")
			}
			w.Write([]byte(`{"login":"owain"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	start, err := c.Start(ctx)
	if err != nil || start.UserCode != "ABCD" {
		t.Fatalf("start: %+v %v", start, err)
	}
	got, err := c.Poll(ctx)
	if err != nil || got.Status != "pending" || poll != 0 {
		t.Fatalf("rate limit: %+v %v", got, err)
	}
	c.nextPoll = time.Time{}
	got, err = c.Poll(ctx)
	if err != nil || got.Status != "pending" || c.interval != 6*time.Second {
		t.Fatalf("slow down: %+v %v", got, err)
	}
	c.nextPoll = time.Time{}
	got, err = c.Poll(ctx)
	if err != nil || got.Status != "connected" {
		t.Fatalf("connect: %+v %v", got, err)
	}
	info, err := os.Stat(filepath.Join(c.dir, "credentials.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	reloaded, err := NewGitHubConnection(filepath.Dir(c.dir), "Iv1.example", c.installURL)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := reloaded.Status(ctx)
	if !status.Connected || status.Account != "owain" {
		t.Fatalf("status: %+v", status)
	}
	if err = c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(c.dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("credentials survived disconnect")
	}
}
func TestGitHubConnectionRepositoryBoundary(t *testing.T) {
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/installations":
			w.Write([]byte(`{"installations":[{"id":42}]}`))
		case "/user/installations/42/repositories":
			w.Write([]byte(`{"repositories":[{"id":1,"full_name":"owner/allowed"}]}`))
		case "/repos/owner/allowed/issues":
			w.Write([]byte(`[]`))
		default:
			t.Fatalf("unauthorized request reached server: %s", r.URL.Path)
		}
	})
	c.credentials.AccessToken = "secret"
	repos, err := c.Repositories(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos: %+v %v", repos, err)
	}
	if _, err = c.API(context.Background(), "GET", "repos/owner/allowed/issues", nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"repos/owner/other/issues", "https://evil.test/steal", "//evil.test/steal", "repos/owner/allowed/../other", "repos/owner/%61llowed/issues", "user/emails"} {
		if _, err = c.API(context.Background(), "GET", path, nil); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}
func TestGitHubConnectionRefreshPreservesCredentials(t *testing.T) {
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"error":"bad_refresh_token"}`)) })
	c.credentials = githubCredentials{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Second)}
	if err := c.save(c.credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := c.token(context.Background()); err == nil {
		t.Fatal("expected refresh failure")
	}
	raw, err := os.ReadFile(filepath.Join(c.dir, "credentials.json"))
	if err != nil || !strings.Contains(string(raw), "old-refresh") || c.credentials.AccessToken != "old" {
		t.Fatal("failed refresh destroyed credentials")
	}
}
func TestGitHubConnectionRejectsRedirect(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	c.credentials.AccessToken = "secret"
	if _, err := c.API(context.Background(), "GET", "user", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked {
		t.Fatal("redirect forwarded credentials")
	}
}
func TestGitHubConnectionDeniedAndExpired(t *testing.T) {
	for _, status := range []string{"access_denied", "expired_token"} {
		t.Run(status, func(t *testing.T) {
			c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"error":"` + status + `"}`)) })
			c.deviceCode = "device"
			c.deadline = time.Now().Add(time.Minute)
			got, err := c.Poll(context.Background())
			if err != nil || got.Status == "pending" || c.deviceCode != "" {
				t.Fatalf("terminal authorization: %+v %v", got, err)
			}
		})
	}
}

func TestGitHubConnectionRefreshIsSerialized(t *testing.T) {
	refreshes := 0
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			refreshes++
			w.Write([]byte(`{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Error("used expired access token")
		}
		w.Write([]byte(`{"login":"owain"}`))
	})
	c.credentials = githubCredentials{AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute)}
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := c.API(context.Background(), "GET", "user", nil); results <- err }()
	}
	for i := 0; i < 8; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if refreshes != 1 {
		t.Fatalf("refreshed %d times", refreshes)
	}
	reloaded, err := NewGitHubConnection(filepath.Dir(c.dir), "different-app", c.installURL)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := reloaded.Status(context.Background())
	if status.Connected {
		t.Fatal("reused a different App's credentials")
	}
}

func TestGitHubConnectionRepositoryWriteReadiness(t *testing.T) {
	for _, scenario := range []struct {
		name, permissions string
		push, ready       bool
	}{
		{"ready", `{"contents":"write","issues":"write","pull_requests":"write","checks":"read","statuses":"read"}`, true, true},
		{"user read only", `{"contents":"write","issues":"write","pull_requests":"write","checks":"write","statuses":"read"}`, false, false},
		{"app read only", `{"contents":"read","issues":"write","pull_requests":"write","checks":"read","statuses":"read"}`, true, false},
		{"checks missing", `{"contents":"write","issues":"write","pull_requests":"write","statuses":"read"}`, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/user/installations" {
					w.Write([]byte(`{"installations":[{"id":1,"permissions":` + scenario.permissions + `}]}`))
					return
				}
				push := "false"
				if scenario.push {
					push = "true"
				}
				w.Write([]byte(`{"repositories":[{"id":1,"full_name":"owner/repo","can_write":true,"permissions":{"push":` + push + `}}]}`))
			})
			c.credentials.AccessToken = "token"
			repos, err := c.Repositories(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != 1 || repos[0].CanWrite != scenario.ready {
				t.Fatalf("readiness: %+v", repos)
			}
			if scenario.ready == (repos[0].AccessError != "") {
				t.Fatalf("access explanation: %+v", repos[0])
			}
		})
	}
}
