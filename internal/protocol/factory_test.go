package protocol

import "testing"

func TestFactoryResultRejectsInvalidRoutingEvidence(t *testing.T) {
	for _, body := range []string{
		`{"outcome":"complete","summary":"ok","factory":{"version":2,"role":"triage"}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"shell"}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"implement","needs_spec":true}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"review","verdict":"accepted","findings":[{"problem":"bad"}]}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"review","verdict":"revision_needed"}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"implement","pr_url":"https://evil.example/pull/1"}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"implement","head_sha":"short"}}`,
		`{"outcome":"complete","summary":"ok","factory":{"version":1,"role":"triage","execute":"shell"}}`,
	} {
		if _, err := ParseStepResult([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, body := range []string{`{"outcome":"complete","summary":"legacy"}`, `{"outcome":"complete","summary":"routed","factory":{"version":1,"role":"triage","needs_spec":true}}`} {
		if _, err := ParseStepResult([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}
