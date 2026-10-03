package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectGitHubEnvironmentRejectsHostedIdentityOverride(t *testing.T) {
	eventPath := filepath.Join(t.TempDir(), "event.json")
	event := `{"pull_request":{"number":7,"title":"change","base":{"sha":"` + strings.Repeat("a", 40) + `","ref":"main","repo":{"full_name":"org/repo"}},"head":{"sha":"` + strings.Repeat("b", 40) + `","ref":"feature","repo":{"full_name":"fork/repo"}}}}`
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_PATH", eventPath)
	t.Setenv("GITHUB_REPOSITORY", "org/repo")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_JOB", "review")
	t.Setenv("GITHUB_SHA", strings.Repeat("c", 40))

	environment, err := DetectEnvironment(EnvironmentOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !environment.UntrustedFork || environment.MergeRevision != strings.Repeat("c", 40) || environment.ChangeID != "7" {
		t.Fatalf("GitHub event identity was not preserved: %#v", environment)
	}
	if _, err := DetectEnvironment(EnvironmentOverrides{HeadRevision: strings.Repeat("d", 40)}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("hosted identity override must fail: %v", err)
	}
}
