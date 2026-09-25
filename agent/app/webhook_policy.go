package app

import (
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

type reviewPolicyDecision struct {
	allowed bool
	reason  string
}

func (s *Server) reviewPolicyDecision(req review.Request) reviewPolicyDecision {
	if s == nil || s.cfg == nil {
		return reviewPolicyDecision{allowed: true, reason: "no policy configured"}
	}
	mode := strings.TrimSpace(s.cfg.WebhookReviewMode)
	if mode == "" {
		mode = "manual_first"
	}
	if mode == "off" {
		return reviewPolicyDecision{reason: "webhook review mode is off"}
	}
	if labelMatches(req.Labels, s.cfg.ReviewLabelExclude) {
		return reviewPolicyDecision{reason: "excluded label matched"}
	}
	if !allowedIdentity(req, s.cfg.ReviewAllowedProjects, s.cfg.ReviewAllowedRepos) {
		return reviewPolicyDecision{reason: "project or repository is not allowed"}
	}
	if branchMatches(req, s.cfg.ReviewBranchExclude) {
		return reviewPolicyDecision{reason: "excluded branch matched"}
	}
	if s.cfg.PolicyV2Mode == "enforce" {
		return reviewPolicyDecision{allowed: true, reason: "trusted V2 trigger evaluation deferred until SCM enrichment"}
	}
	if len(s.cfg.ReviewBranchInclude) > 0 && !branchMatches(req, s.cfg.ReviewBranchInclude) {
		return reviewPolicyDecision{reason: "no included branch matched"}
	}
	if mode == "manual_first" && !labelMatches(req.Labels, s.cfg.ReviewLabelInclude) {
		return reviewPolicyDecision{reason: "no included review label matched"}
	}
	return reviewPolicyDecision{allowed: true, reason: "policy allowed"}
}

func allowedIdentity(req review.Request, projects []string, repos []string) bool {
	project := strings.TrimSpace(req.ProjectID)
	repo := strings.TrimSpace(firstNonEmptyString(req.Repository, req.ProjectID))
	if len(projects) > 0 && !stringListContains(projects, project) {
		return false
	}
	if len(repos) > 0 && !stringListContains(repos, repo) {
		return false
	}
	return true
}

func labelMatches(labels []string, policy []string) bool {
	if len(labels) == 0 || len(policy) == 0 {
		return false
	}
	for _, label := range labels {
		if stringListContainsFold(policy, label) {
			return true
		}
	}
	return false
}

func branchMatches(req review.Request, policy []string) bool {
	if len(policy) == 0 {
		return false
	}
	return stringListContains(policy, req.SourceBranch) || stringListContains(policy, req.TargetBranch)
}

func stringListContains(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func stringListContainsFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
