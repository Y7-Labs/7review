package app

import (
	"net/http"
	"testing"

	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestChannelDelivery_DuplicateCallbackQueuesOnce(t *testing.T) {
	// Given a known run and the same authenticated provider callback delivered twice.
	s, msg := channelDeliveryFixture(t)
	for i := 0; i < 2; i++ {
		_, status, err := s.routeChannelMessageContext(t.Context(), msg)
		if err != nil || status != http.StatusAccepted {
			t.Fatalf("delivery %d: %d %v", i, status, err)
		}
	}
	// Then the callback creates only one action and one received-message event.
	if len(s.work) != 1 {
		t.Fatalf("duplicate callback queued %d actions", len(s.work))
	}
	run, err := s.pipeline.Jobs.Get(t.Context(), msg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range run.Events {
		if event.Type == "channel_message_received" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("received event recorded %d times", count)
	}
}

func TestChannelDelivery_QueueFullCanRetry(t *testing.T) {
	s, msg := channelDeliveryFixture(t)
	s.work <- workItem{name: "busy"}
	// Given a full queue, delivery must fail visibly and remain retryable.
	_, status, err := s.routeChannelMessageContext(t.Context(), msg)
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("expected queue rejection, got %d %v", status, err)
	}
	<-s.work
	_, status, err = s.routeChannelMessageContext(t.Context(), msg)
	if err != nil || status != http.StatusAccepted || len(s.work) != 1 {
		t.Fatalf("retry lost: %d %v queue=%d", status, err, len(s.work))
	}
}

func channelDeliveryFixture(t *testing.T) (*Server, channel.InboundMessage) {
	t.Helper()
	jobs := pipeline.NewMemoryRunStore()
	run, err := jobs.Start(t.Context(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{pipeline: &pipeline.Pipeline{Jobs: jobs}, work: make(chan workItem, 1)},
		channel.InboundMessage{Channel: "telegram", ExternalID: "update-1", RunID: run.ID, SenderID: "123", Text: "/approve " + run.ID}
}
