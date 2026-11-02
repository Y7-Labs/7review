package review

import "time"

// ModelJobProjection is a durable local result, never approval or publication.
type ModelJobProjection struct {
	RunID       string
	Succeeded   bool
	Source      Source
	Reason      string
	CompletedAt time.Time
}
