package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestAuthenticatedWebhookDigestIncludesUnnormalizedPayloadFields(t *testing.T) {
	var captured []review.Request
	handler := gitHubWebhookHandler("secret", func(r review.Request) (reviewDispatchResult, error) {
		captured = append(captured, r)
		return reviewDispatchResult{Status: "enqueued"}, nil
	})
	first := `{"action":"opened","number":7,"repository":{"full_name":"org/repo"},"pull_request":{},"extra":"one"}`
	second := strings.Replace(first, `"one"`, `"two"`, 1)
	for _, body := range []string{first, second} {
		r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", githubSignature("secret", body))
		r.Header.Set("X-GitHub-Delivery", "same-id")
		r.Header.Set("X-GitHub-Event", "pull_request")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Fatalf("valid fixture webhook rejected: %d %s", w.Code, w.Body.String())
		}
	}
	if len(captured) != 2 || captured[0].PayloadDigest != canonicalPayloadDigest([]byte(first)) || captured[0].PayloadDigest == captured[1].PayloadDigest {
		t.Fatal("normalization erased payload conflict identity")
	}
}
