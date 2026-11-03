package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/channel"
)

type nativeSCMComment struct {
	Provider   string
	ActorID    string
	ActorName  string
	ProjectID  string
	ChangeID   string
	CommentID  string
	DeliveryID string
	Text       string
}

type nativeSCMCommentHandler func(context.Context, nativeSCMComment) (channel.InboundResult, int, error)

func handleGitHubIssueComment(w http.ResponseWriter, r *http.Request, body []byte, handlers []nativeSCMCommentHandler) {
	var event struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Issue struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Sender struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"sender"`
		Comment struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if event.Action != "created" || len(event.Issue.PullRequest) == 0 || string(event.Issue.PullRequest) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if event.Repository.FullName == "" || event.Issue.Number <= 0 || event.Sender.ID <= 0 || event.Comment.ID <= 0 {
		http.Error(w, "missing pull request comment identity", http.StatusBadRequest)
		return
	}
	dispatchNativeSCMComment(w, r, handlers, nativeSCMComment{
		Provider:   "github",
		ActorID:    strconv.FormatInt(event.Sender.ID, 10),
		ActorName:  strings.TrimSpace(event.Sender.Login),
		ProjectID:  event.Repository.FullName,
		ChangeID:   strconv.Itoa(event.Issue.Number),
		CommentID:  strconv.FormatInt(event.Comment.ID, 10),
		DeliveryID: strings.TrimSpace(r.Header.Get("X-GitHub-Delivery")),
		Text:       event.Comment.Body,
	})
}

func handleGitLabNote(w http.ResponseWriter, r *http.Request, body []byte, handlers []nativeSCMCommentHandler) {
	var event struct {
		ObjectKind string `json:"object_kind"`
		EventType  string `json:"event_type"`
		UserID     int64  `json:"user_id"`
		User       struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
		MergeRequest struct {
			IID int `json:"iid"`
		} `json:"merge_request"`
		ObjectAttributes struct {
			ID           int64  `json:"id"`
			NoteableType string `json:"noteable_type"`
			Note         string `json:"note"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(event.ObjectAttributes.NoteableType), "MergeRequest") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	actorID := event.UserID
	if event.UserID > 0 && event.User.ID > 0 && event.UserID != event.User.ID {
		http.Error(w, "conflicting merge request note actor identity", http.StatusBadRequest)
		return
	}
	if actorID == 0 {
		actorID = event.User.ID
	}
	if event.Project.ID <= 0 || event.MergeRequest.IID <= 0 || actorID <= 0 || event.ObjectAttributes.ID <= 0 {
		http.Error(w, "missing merge request note identity", http.StatusBadRequest)
		return
	}
	dispatchNativeSCMComment(w, r, handlers, nativeSCMComment{
		Provider:   "gitlab",
		ActorID:    strconv.FormatInt(actorID, 10),
		ActorName:  strings.TrimSpace(event.User.Username),
		ProjectID:  strconv.FormatInt(event.Project.ID, 10),
		ChangeID:   strconv.Itoa(event.MergeRequest.IID),
		CommentID:  strconv.FormatInt(event.ObjectAttributes.ID, 10),
		DeliveryID: firstNonEmptyString(strings.TrimSpace(r.Header.Get("X-Gitlab-Event-UUID")), strings.TrimSpace(r.Header.Get("X-Gitlab-Delivery"))),
		Text:       event.ObjectAttributes.Note,
	})
}

func dispatchNativeSCMComment(w http.ResponseWriter, r *http.Request, handlers []nativeSCMCommentHandler, comment nativeSCMComment) {
	if len(handlers) == 0 || handlers[0] == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, status, err := handlers[0](r.Context(), comment)
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	if err != nil {
		if status < 400 {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	if status >= 400 {
		http.Error(w, errors.New(http.StatusText(status)).Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
