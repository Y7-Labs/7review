package app

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

type reviewRequestHandler func(review.Request) (reviewDispatchResult, error)

func gitLabWebhookHandler(secret string, handler reviewRequestHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret != "" && !validGitLabToken(secret, r.Header.Get("X-Gitlab-Token")) {
			http.Error(w, "invalid webhook token", http.StatusUnauthorized)
			return
		}
		body, err := readWebhookBody(r.Body)
		if err != nil {
			http.Error(w, "webhook payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		var event struct {
			ObjectKind string       `json:"object_kind"`
			EventType  string       `json:"event_type"`
			Labels     gitLabLabels `json:"labels"`
			Project    struct {
				ID int `json:"id"`
			} `json:"project"`
			ObjectAttributes struct {
				IID          int    `json:"iid"`
				Action       string `json:"action"`
				Title        string `json:"title"`
				Description  string `json:"description"`
				URL          string `json:"url"`
				SourceBranch string `json:"source_branch"`
				TargetBranch string `json:"target_branch"`
				LastCommit   struct {
					ID string `json:"id"`
				} `json:"last_commit"`
				Labels gitLabLabels `json:"labels"`
				Draft  bool         `json:"draft"`
			} `json:"object_attributes"`
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid webhook payload", http.StatusBadRequest)
			return
		}
		if !isGitLabMergeRequestEvent(event.ObjectKind, event.EventType) || !reviewableGitLabAction(event.ObjectAttributes.Action) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if event.Project.ID == 0 || event.ObjectAttributes.IID == 0 {
			http.Error(w, "missing project or merge request id", http.StatusBadRequest)
			return
		}

		result, err := handler(review.Request{
			Provider:     "gitlab",
			DeliveryID:   firstNonEmptyString(r.Header.Get("X-Gitlab-Event-UUID"), r.Header.Get("X-Gitlab-Delivery")),
			EventAction:  event.ObjectAttributes.Action,
			ProjectID:    strconv.Itoa(event.Project.ID),
			MRIID:        event.ObjectAttributes.IID,
			ChangeID:     strconv.Itoa(event.ObjectAttributes.IID),
			Title:        event.ObjectAttributes.Title,
			Description:  event.ObjectAttributes.Description,
			WebURL:       event.ObjectAttributes.URL,
			SourceSHA:    event.ObjectAttributes.LastCommit.ID,
			SourceBranch: event.ObjectAttributes.SourceBranch,
			TargetBranch: event.ObjectAttributes.TargetBranch,
			Author:       event.User.Username,
			Draft:        event.ObjectAttributes.Draft,
			Labels:       mergeGitLabLabels(event.ObjectAttributes.Labels, event.Labels),
		})
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

type gitLabLabels []string

func (labels *gitLabLabels) UnmarshalJSON(data []byte) error {
	var stringLabels []string
	if err := json.Unmarshal(data, &stringLabels); err == nil {
		*labels = compactGitLabLabels(stringLabels)
		return nil
	}

	var objectLabels []struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(data, &objectLabels); err == nil {
		var out []string
		for _, label := range objectLabels {
			out = append(out, firstNonEmptyString(label.Title, label.Name))
		}
		*labels = compactGitLabLabels(out)
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*labels = compactGitLabLabels([]string{single})
		return nil
	}

	*labels = nil
	return nil
}

func mergeGitLabLabels(groups ...gitLabLabels) []string {
	var merged []string
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return compactGitLabLabels(merged)
}

func compactGitLabLabels(labels []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

func validGitLabToken(expected, provided string) bool {
	expected = strings.TrimSpace(expected)
	provided = strings.TrimSpace(provided)
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func isGitLabMergeRequestEvent(objectKind, eventType string) bool {
	objectKind = strings.ToLower(strings.TrimSpace(objectKind))
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	return objectKind == "merge_request" || eventType == "merge_request"
}

func reviewableGitLabAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "open", "reopen", "update", "approved", "approval", "unapproved", "unapproval", "merge":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
