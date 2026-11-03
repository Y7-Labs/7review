package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/review"
)

func (s *Server) routeFindingDisposition(ctx context.Context, result channel.InboundResult, actor review.OperatorActor, change *review.ChangeKey) (channel.InboundResult, int, error) {
	if s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
		return result, http.StatusServiceUnavailable, errors.New("durable operator authority unavailable")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var view *review.FindingDispositionView
	var err error
	if change == nil {
		view, err = s.accountingRuntime.Authority.FindingDisposition(commandCtx, actor, result.AttemptID, result.FindingID)
	} else {
		view, err = s.accountingRuntime.Authority.FindingDispositionInChange(commandCtx, actor, result.AttemptID, result.FindingID, *change)
	}
	if err == nil {
		expectedVersion := view.ControlVersion
		decision := review.FindingDecision(result.Decision)
		var command review.FindingDispositionCommand
		if view.Current == nil || view.Current.CommandID != result.CommandID {
			var prior *review.FindingDispositionCommand
			prior, err = s.accountingRuntime.Authority.PriorFindingDispositionCommand(commandCtx, actor, result.CommandID, result.AttemptID, result.FindingID, decision, result.Request)
			if err != nil {
				return result, findingDispositionErrorStatus(err), err
			}
			if prior != nil {
				command = *prior
			}
		}
		if view.Current != nil && view.Current.CommandID == result.CommandID {
			if view.Current.Decision != decision || view.Current.Reason != result.Request || view.Current.RecordedVersion < 2 {
				return result, http.StatusConflict, errors.New("command ID already identifies a different finding disposition")
			}
			expectedVersion = view.Current.RecordedVersion - 1
		}
		if command.CommandID == "" {
			command = review.FindingDispositionCommand{SchemaVersion: 1, CommandID: result.CommandID, AttemptID: result.AttemptID, ExpectedVersion: expectedVersion, Revision: view.Revision, Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: result.FindingID, FindingDigest: view.FindingDigest, Decision: decision, Reason: result.Request}}
		}
		var receipt *review.FindingDispositionReceipt
		receipt, err = s.accountingRuntime.Authority.DispositionFinding(commandCtx, actor, command)
		if err == nil {
			result.Dispositioned = true
			result.ActorID = receipt.ActorID
			result.ControlVersion = receipt.ControlVersion
			result.Reason = ""
			return result, http.StatusOK, nil
		}
	}
	return result, findingDispositionErrorStatus(err), err
}

func findingDispositionErrorStatus(err error) int {
	status := http.StatusServiceUnavailable
	var conflict *accounting.OperatorCommandConflict
	switch {
	case errors.As(err, &conflict), errors.Is(err, accounting.ErrOperatorCommandConflict), errors.Is(err, accounting.ErrOperatorAttemptContext):
		status = http.StatusConflict
	case errors.Is(err, review.ErrOperatorUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, review.ErrOperatorForbidden):
		status = http.StatusForbidden
	case errors.Is(err, accounting.ErrOperatorAttemptNotFound), errors.Is(err, accounting.ErrOperatorFindingNotFound):
		status = http.StatusNotFound
	case errors.Is(err, accounting.ErrOperatorAttemptInvalid):
		status = http.StatusBadRequest
	}
	return status
}

func (s *Server) handleNativeSCMComment(ctx context.Context, comment nativeSCMComment) (channel.InboundResult, int, error) {
	result := channel.InboundResult{Channel: "scm_" + comment.Provider, Sender: comment.ActorID}
	if s == nil || s.cfg == nil || s.cfg.Operators == nil {
		return result, http.StatusServiceUnavailable, errors.New("operator authority unavailable")
	}
	botName := ""
	endpoint := ""
	switch comment.Provider {
	case "github":
		botName, endpoint = s.cfg.GitHubBotLogin, s.cfg.GitHubAPIURL
	case "gitlab":
		botName, endpoint = s.cfg.GitLabBotUsername, s.cfg.GitLabURL
	default:
		return result, http.StatusBadRequest, errors.New("unsupported SCM comment provider")
	}
	if botName == "" {
		return result, http.StatusAccepted, nil
	}
	if strings.EqualFold(strings.TrimSpace(comment.ActorName), strings.TrimSpace(strings.TrimPrefix(botName, "@"))) {
		result.Reason = "self-authored bot comment ignored"
		return result, http.StatusAccepted, nil
	}
	msg, mentioned, err := nativeSCMDispositionMessage(botName, comment)
	if err != nil {
		return result, http.StatusBadRequest, err
	}
	if !mentioned {
		result.Reason = "comment does not explicitly address the configured bot"
		return result, http.StatusAccepted, nil
	}
	result = channel.EvaluateInbound(msg)
	if result.Command != "disposition-finding" || result.Reason != "" {
		return result, http.StatusBadRequest, errors.New(result.Reason)
	}
	envName, ok := s.cfg.SCMOperatorTokenEnvs[comment.Provider+":"+comment.ActorID]
	if !ok || envName == "" {
		return result, http.StatusUnauthorized, errors.New("SCM actor is not mapped to an operator")
	}
	credential, ok := os.LookupEnv(envName)
	if !ok || credential == "" {
		return result, http.StatusUnauthorized, errors.New("mapped operator credential unavailable")
	}
	actor, err := s.cfg.Operators.Authenticate(credential)
	if err != nil {
		return result, http.StatusUnauthorized, err
	}
	host, err := review.ProviderHost(comment.Provider, endpoint)
	if err != nil {
		return result, http.StatusServiceUnavailable, err
	}
	deliveryID := comment.DeliveryID
	if deliveryID == "" {
		deliveryID = comment.ProjectID + ":" + comment.CommentID
	}
	deliveryKey := "native-scm:" + comment.Provider + ":" + deliveryID
	if !s.claimDelivery(deliveryKey) {
		result.Reason = "duplicate SCM comment delivery"
		return result, http.StatusAccepted, nil
	}
	accepted := false
	defer func() {
		if !accepted {
			s.releaseDelivery(deliveryKey)
		}
	}()
	change := review.ChangeKey{Provider: comment.Provider, Host: host, RepositoryID: comment.ProjectID, ChangeNumber: comment.ChangeID}
	result, status, err := s.routeFindingDisposition(ctx, result, actor, &change)
	if err == nil {
		accepted = true
	}
	return result, status, err
}
