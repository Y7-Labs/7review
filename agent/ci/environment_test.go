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

func TestHostedGitHubIntakeRequiresTargetIdentityAndForkProvenance(t *testing.T) {
	for _, mode := range []string{"wrong target", "missing source", "missing number"} {
		t.Run(mode, func(t *testing.T) {
			baseRepo, headRepo, number := "org/repo", "org/repo", "7"
			switch mode {
			case "wrong target":
				baseRepo = "other/repo"
			case "missing source":
				headRepo = ""
			case "missing number":
				number = "0"
			}
			event := `{"pull_request":{"number":` + number + `,"base":{"sha":"base","repo":{"full_name":"` + baseRepo + `"}},"head":{"sha":"head","repo":{"full_name":"` + headRepo + `"}}}}`
			path := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(path, []byte(event), 0600); err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITLAB_CI": "false", "GITHUB_REPOSITORY": "org/repo", "GITHUB_RUN_ID": "42", "GITHUB_JOB": "review", "GITHUB_EVENT_PATH": path, "GITHUB_SERVER_URL": "https://ghe.example"} {
				t.Setenv(key, value)
			}
			env, err := DetectEnvironment(EnvironmentOverrides{})
			if mode == "missing source" {
				if err != nil || !env.UntrustedFork || env.Host != "ghe.example" {
					t.Fatalf("missing fork provenance enabled trusted CI: %#v %v", env, err)
				}
			} else if err == nil {
				t.Fatalf("invalid hosted target became canonical: %#v", env)
			}
		})
	}
}

func TestHostedGitLabIntakeUsesStableProjectIDAndRejectsUnknownForkAuthority(t *testing.T) {
	for key, value := range map[string]string{"GITHUB_ACTIONS": "false", "GITLAB_CI": "true", "CI_SERVER_URL": "https://gitlab.example", "CI_PROJECT_ID": "25", "CI_PROJECT_PATH": "team/renamable-path", "CI_PIPELINE_ID": "42", "CI_JOB_ID": "review", "CI_COMMIT_SHA": "head", "CI_MERGE_REQUEST_DIFF_BASE_SHA": "base", "CI_MERGE_REQUEST_IID": "7", "CI_MERGE_REQUEST_SOURCE_PROJECT_ID": ""} {
		t.Setenv(key, value)
	}
	env, err := DetectEnvironment(EnvironmentOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if env.RepositoryID != "25" || !env.UntrustedFork {
		t.Fatalf("CI bypassed service project scope or trusted unknown fork: %#v", env)
	}
	t.Setenv("CI_MERGE_REQUEST_SOURCE_PROJECT_ID", "25")
	env, err = DetectEnvironment(EnvironmentOverrides{})
	if err != nil || env.UntrustedFork {
		t.Fatalf("verified same-project CI was not recognized: %#v %v", env, err)
	}
}
