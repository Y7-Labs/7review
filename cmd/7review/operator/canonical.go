package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

const maxCanonicalResponseBytes = 16384

type CanonicalRequestError struct {
	StatusCode       int
	Conflict         *review.OperatorCommandConflictView
	MayHaveCommitted bool
	CommandID        string
	message          string
	cause            error
}

func (e *CanonicalRequestError) Error() string {
	message := e.message
	if message == "" {
		message = "operator request failed"
	}
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Conflict != nil {
		message += fmt.Sprintf("; current attempt %s, control version %d; inspect explicitly, command was not retargeted", e.Conflict.CurrentAttemptID, e.Conflict.ControlVersion)
	}
	if e.MayHaveCommitted {
		message += "; operator command outcome unconfirmed: retry the identical command ID and payload with the same principal"
	}
	return message
}
func (e *CanonicalRequestError) Unwrap() error { return e.cause }

func (c Client) Attempt(ctx context.Context, id string) (review.OperatorAttemptView, error) {
	var empty review.OperatorAttemptView
	if err := review.ValidateOperatorAttemptID(id); err != nil {
		return empty, err
	}
	_, raw, err := c.canonicalRequest(ctx, http.MethodGet, "attempt", url.Values{"attempt_id": {id}}, nil)
	if err != nil {
		return empty, err
	}
	v, err := review.DecodeOperatorAttemptView(raw, id)
	if err != nil {
		return empty, &CanonicalRequestError{StatusCode: 200, message: "operator returned an unproven attempt response"}
	}
	return v, nil
}

func (c Client) CancelAttempt(ctx context.Context, command review.CancelAttemptCommand) (review.CancelAttemptReceipt, error) {
	var empty review.CancelAttemptReceipt
	if err := command.Validate(); err != nil {
		return empty, err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return empty, err
	}
	if len(payload) > review.MaxOperatorCommandBytes {
		return empty, errors.New("operator command exceeds transport bound")
	}
	_, raw, err := c.canonicalRequest(ctx, http.MethodPost, "commands", nil, payload)
	if err != nil {
		var failure *CanonicalRequestError
		if errors.As(err, &failure) {
			failure.CommandID = command.CommandID
		}
		return empty, err
	}
	r, err := review.DecodeCancelAttemptReceipt(raw, command)
	if err != nil {
		return empty, &CanonicalRequestError{StatusCode: 200, MayHaveCommitted: true, CommandID: command.CommandID, message: "operator returned an unproven cancellation receipt"}
	}
	return r, nil
}

func (c Client) AuthorizePublication(ctx context.Context, command review.AuthorizePublicationCommand) (review.PublicationAuthorizationReceipt, error) {
	var empty review.PublicationAuthorizationReceipt
	if err := command.Validate(); err != nil {
		return empty, err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return empty, err
	}
	if len(payload) > review.MaxOperatorCommandBytes {
		return empty, errors.New("operator command exceeds transport bound")
	}
	_, raw, err := c.canonicalRequest(ctx, http.MethodPost, "commands", nil, payload)
	if err != nil {
		var failure *CanonicalRequestError
		if errors.As(err, &failure) {
			failure.CommandID = command.CommandID
		}
		return empty, err
	}
	r, err := review.DecodePublicationAuthorizationReceipt(raw, command)
	if err != nil {
		return empty, &CanonicalRequestError{StatusCode: http.StatusOK, MayHaveCommitted: true, CommandID: command.CommandID, message: "operator returned an unproven publication authorization receipt"}
	}
	return r, nil
}

func (c Client) DispositionFinding(ctx context.Context, command review.FindingDispositionCommand) (review.FindingDispositionReceipt, error) {
	var empty review.FindingDispositionReceipt
	if err := command.Validate(); err != nil {
		return empty, err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return empty, err
	}
	if len(payload) > review.MaxOperatorCommandBytes {
		return empty, errors.New("operator command exceeds transport bound")
	}
	_, raw, err := c.canonicalRequest(ctx, http.MethodPost, "commands", nil, payload)
	if err != nil {
		var failure *CanonicalRequestError
		if errors.As(err, &failure) {
			failure.CommandID = command.CommandID
		}
		return empty, err
	}
	r, err := review.DecodeFindingDispositionReceipt(raw, command)
	if err != nil {
		return empty, &CanonicalRequestError{StatusCode: http.StatusOK, MayHaveCommitted: true, CommandID: command.CommandID, message: "operator returned an unproven finding disposition receipt"}
	}
	return r, nil
}

func canonicalBaseURL(server string) (*url.URL, error) {
	u, err := url.Parse(server)
	invalid := errors.New("operator server requires HTTPS or loopback HTTP, without credentials, query or fragment")
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, invalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (!strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback())) {
			return nil, invalid
		}
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, invalid
		}
	}
	return u, nil
}

func canonicalCredential() (string, error) {
	token := os.Getenv("REVIEW_OPERATOR_TOKEN")
	invalid := errors.New("REVIEW_OPERATOR_TOKEN must contain an explicitly mapped operator credential")
	if len(token) == 0 || len(token) > 4096 {
		return "", invalid
	}
	for _, r := range token {
		if r < 33 || r > 126 {
			return "", invalid
		}
	}
	return token, nil
}

func (c Client) canonicalRequest(ctx context.Context, method, endpoint string, query url.Values, payload []byte) (int, []byte, error) {
	if ctx == nil {
		return 0, nil, errors.New("operator request context required")
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, &CanonicalRequestError{message: "operator request cancelled before dispatch", cause: err}
	}
	base, err := canonicalBaseURL(c.ServerURL)
	if err != nil {
		return 0, nil, err
	}
	token, err := canonicalCredential()
	if err != nil {
		return 0, nil, err
	}
	u := base.JoinPath("operator", endpoint)
	u.RawQuery = query.Encode()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, errors.New("invalid operator request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: defaultRequestTimeout}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	if client.Timeout <= 0 || client.Timeout > defaultRequestTimeout {
		client.Timeout = defaultRequestTimeout
	}
	// Redirects can change method/target. Never forward this credential or mutate
	// the shared client used by legacy commands and streaming terminal views.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	resp, err := client.Do(req)
	mutation := method == http.MethodPost
	if err != nil {
		var cause error
		if errors.Is(err, context.Canceled) {
			cause = context.Canceled
		} else if errors.Is(err, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		}
		return 0, nil, &CanonicalRequestError{MayHaveCommitted: mutation, message: "operator transport failed", cause: cause}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCanonicalResponseBytes+1))
	mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	failure := &CanonicalRequestError{StatusCode: resp.StatusCode, MayHaveCommitted: mutation}
	switch resp.StatusCode {
	case 400, 401, 403, 404, 405, 409, 413, 415:
		failure.MayHaveCommitted = false
	}
	if readErr != nil || len(raw) > maxCanonicalResponseBytes {
		failure.message = "operator returned an unreadable response"
		return resp.StatusCode, nil, failure
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusConflict && mediaErr == nil && mediaType == "application/json" {
			if conflict, err := review.DecodeOperatorCommandConflict(raw); err == nil {
				failure.Conflict = &conflict
			}
		}
		return resp.StatusCode, nil, failure
	}
	if mediaErr != nil || mediaType != "application/json" {
		failure.message = "operator returned an unsupported response"
		return resp.StatusCode, nil, failure
	}
	return resp.StatusCode, raw, nil
}
