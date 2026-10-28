package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type identifiedRunStore interface {
	StartIdentified(context.Context, review.Request, string) (*Run, error)
}

func identifiedRun(req review.Request, id string) (*Run, error) {
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		return nil, fmt.Errorf("invalid canonical run ID")
	}
	run := &Run{ID: id, Request: req, Status: StatusRunning, UpdatedAt: time.Now().UTC()}
	appendRunEvent(run, "run_started", StatusRunning, "canonical execution", nil)
	return run, nil
}
func (s *MemoryRunStore) StartIdentified(ctx context.Context, req review.Request, id string) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[id]; exists {
		return nil, fmt.Errorf("canonical run already exists")
	}
	run, err := identifiedRun(req, id)
	if err != nil {
		return nil, err
	}
	s.runs[id] = run
	return run, nil
}
func (s *FileRunStore) StartIdentified(ctx context.Context, req review.Request, id string) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := identifiedRun(req, id)
	if err != nil {
		return nil, err
	}
	if err = s.ensureDir(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	return s.copyRun(run), nil
}
