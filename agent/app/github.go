package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

func gitHubWebhookHandler(secret string, handler reviewRequestHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := readWebhookBody(r.Body)
		if err != nil {
			http.Error(w, "webhook payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if secret != "" && !validGitHubSignature(secret, r.Header.Get("X-Hub-Signature-256"), body) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-GitHub-Event") != "pull_request" {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var event struct {
			Action      string            `json:"action"`
			Number      int               `json:"number"`
			Repository  githubRepository  `json:"repository"`
			PullRequest githubPullRequest `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid webhook payload", http.StatusBadRequest)
			return
		}
		if event.Repository.FullName == "" || event.Number == 0 {
			http.Error(w, "missing repository or pull request number", http.StatusBadRequest)
			return
		}
		if !reviewableGitHubAction(event.Action) {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		req := review.Request{
			Provider:     "github",
			DeliveryID:   r.Header.Get("X-GitHub-Delivery"),
			EventAction:  event.Action,
			ProjectID:    event.Repository.FullName,
			MRIID:        event.Number,
			Repository:   event.Repository.FullName,
			ChangeID:     strconv.Itoa(event.Number),
			Title:        event.PullRequest.Title,
			Description:  event.PullRequest.Body,
			WebURL:       event.PullRequest.HTMLURL,
			SourceSHA:    event.PullRequest.Head.SHA,
			TargetSHA:    event.PullRequest.Base.SHA,
			SourceBranch: event.PullRequest.Head.Ref,
			TargetBranch: event.PullRequest.Base.Ref,
			Author:       event.PullRequest.User.Login,
			Draft:        event.PullRequest.Draft,
		}
		for _, label := range event.PullRequest.Labels {
			req.Labels = append(req.Labels, label.Name)
		}

		result, err := handler(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		if result.Status == "ignored" && result.Reason != "" {
			_, _ = w.Write([]byte(result.Reason))
		}
	}
}

func validGitHubSignature(secret, signature string, body []byte) bool {
	signature = strings.TrimPrefix(signature, "sha256=")
	if signature == "" {
		return false
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func reviewableGitHubAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "ready_for_review":
		return true
	default:
		return false
	}
}

type githubRepository struct {
	FullName string `json:"full_name"`
}

type githubPullRequest struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}
