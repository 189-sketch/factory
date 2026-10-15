package managedworker

import "testing"

func TestRecoverPublicationDoesNotForgetLostPRResponse(t *testing.T) {
	base := "0123456789012345678901234567890123456789"
	branch := "codex/task-test"
	got, err := recoverPublication(base+"\trefs/heads/"+branch+"\n", base, branch, "")
	if err != nil || !got {
		t.Fatalf("lost-response publication not recovered: %v", err)
	}
	if _, err = recoverPublication(base+"\trefs/heads/"+branch, base, branch, "https://github.com/example/repo/pull/1"); err == nil {
		t.Fatal("no-change repair accepted")
	}
	if got, err = recoverPublication("", base, branch, ""); err != nil || got {
		t.Fatal("operational task became a PR")
	}
	if _, err = recoverPublication(base+"\trefs/heads/another", base, branch, ""); err == nil {
		t.Fatal("another branch accepted")
	}
}
