package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Environment struct {
	Provider      string
	RepositoryID  string
	BaseRevision  string
	HeadRevision  string
	MergeRevision string
	PipelineID    string
	JobID         string
	ChangeID      string
	Title         string
	Description   string
	WebURL        string
	SourceBranch  string
	TargetBranch  string
	Author        string
	UntrustedFork bool
	Hosted        bool
}

type EnvironmentOverrides struct {
	Provider, RepositoryID, BaseRevision, HeadRevision, MergeRevision string
	PipelineID, JobID, ChangeID                                       string
}

func DetectEnvironment(overrides EnvironmentOverrides) (Environment, error) {
	var detected Environment
	var err error
	switch {
	case strings.EqualFold(os.Getenv("GITHUB_ACTIONS"), "true"):
		detected, err = detectGitHubEnvironment()
	case strings.EqualFold(os.Getenv("GITLAB_CI"), "true"):
		detected, err = detectGitLabEnvironment()
	default:
		detected = Environment{}
	}
	if err != nil {
		return Environment{}, err
	}
	if err := applyOverrides(&detected, overrides); err != nil {
		return Environment{}, err
	}
	if detected.Provider != "github" && detected.Provider != "gitlab" {
		return Environment{}, errors.New("CI provider must be github or gitlab")
	}
	for name, value := range map[string]string{
		"repository": detected.RepositoryID, "base revision": detected.BaseRevision,
		"head revision": detected.HeadRevision, "pipeline ID": detected.PipelineID,
	} {
		if strings.TrimSpace(value) == "" {
			return Environment{}, fmt.Errorf("CI %s is required", name)
		}
	}
	if detected.ChangeID == "" {
		detected.ChangeID = detected.PipelineID
	}
	return detected, nil
}

func detectGitHubEnvironment() (Environment, error) {
	env := Environment{
		Provider: "github", RepositoryID: os.Getenv("GITHUB_REPOSITORY"),
		PipelineID: os.Getenv("GITHUB_RUN_ID"), JobID: os.Getenv("GITHUB_JOB"),
		MergeRevision: os.Getenv("GITHUB_SHA"), Hosted: true,
	}
	path := strings.TrimSpace(os.Getenv("GITHUB_EVENT_PATH"))
	if path == "" {
		return env, errors.New("GITHUB_EVENT_PATH is required for immutable pull request identity")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return env, fmt.Errorf("read GitHub event: %w", err)
	}
	var event struct {
		PullRequest struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			HTMLURL string `json:"html_url"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Base struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
			Head struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return env, fmt.Errorf("decode GitHub event: %w", err)
	}
	pr := event.PullRequest
	env.BaseRevision, env.HeadRevision = pr.Base.SHA, pr.Head.SHA
	env.ChangeID = strconv.Itoa(pr.Number)
	env.Title, env.Description, env.WebURL, env.Author = pr.Title, pr.Body, pr.HTMLURL, pr.User.Login
	env.SourceBranch, env.TargetBranch = pr.Head.Ref, pr.Base.Ref
	env.UntrustedFork = pr.Head.Repo.FullName != "" && pr.Base.Repo.FullName != "" && !strings.EqualFold(pr.Head.Repo.FullName, pr.Base.Repo.FullName)
	if env.MergeRevision == env.HeadRevision || env.MergeRevision == env.BaseRevision {
		env.MergeRevision = ""
	}
	return env, nil
}

func detectGitLabEnvironment() (Environment, error) {
	head := firstEnvironment("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "CI_COMMIT_SHA")
	comparison := os.Getenv("CI_COMMIT_SHA")
	if comparison == head || comparison == os.Getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA") {
		comparison = ""
	}
	return Environment{
		Provider: "gitlab", RepositoryID: firstEnvironment("CI_PROJECT_PATH", "CI_PROJECT_ID"),
		BaseRevision: firstEnvironment("CI_MERGE_REQUEST_DIFF_BASE_SHA", "CI_MERGE_REQUEST_TARGET_BRANCH_SHA"),
		HeadRevision: head, MergeRevision: comparison, PipelineID: os.Getenv("CI_PIPELINE_ID"), JobID: os.Getenv("CI_JOB_ID"),
		ChangeID: os.Getenv("CI_MERGE_REQUEST_IID"), Title: os.Getenv("CI_MERGE_REQUEST_TITLE"),
		WebURL: os.Getenv("CI_MERGE_REQUEST_PROJECT_URL"), SourceBranch: os.Getenv("CI_MERGE_REQUEST_SOURCE_BRANCH_NAME"),
		TargetBranch: os.Getenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME"), Author: os.Getenv("GITLAB_USER_LOGIN"), Hosted: true,
		UntrustedFork: sourceProjectDiffers(),
	}, nil
}

func sourceProjectDiffers() bool {
	source := os.Getenv("CI_MERGE_REQUEST_SOURCE_PROJECT_ID")
	target := os.Getenv("CI_PROJECT_ID")
	return source != "" && target != "" && source != target
}

func applyOverrides(env *Environment, values EnvironmentOverrides) error {
	fields := []struct {
		name   string
		target *string
		value  string
	}{
		{"provider", &env.Provider, values.Provider}, {"repository", &env.RepositoryID, values.RepositoryID},
		{"base revision", &env.BaseRevision, values.BaseRevision}, {"head revision", &env.HeadRevision, values.HeadRevision},
		{"merge revision", &env.MergeRevision, values.MergeRevision}, {"pipeline ID", &env.PipelineID, values.PipelineID},
		{"job ID", &env.JobID, values.JobID}, {"change ID", &env.ChangeID, values.ChangeID},
	}
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		if env.Hosted && *field.target != "" && *field.target != field.value {
			return fmt.Errorf("CI %s override does not match the hosted environment", field.name)
		}
		*field.target = field.value
	}
	return nil
}

func firstEnvironment(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
