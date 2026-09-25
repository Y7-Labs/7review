package review

// Request is the normalized input for one merge request review run.
// Adapters such as GitLab webhooks should convert provider payloads into this
// shape before entering the pipeline.
type Request struct {
	Provider     string
	DeliveryID   string
	EventAction  string
	ProjectID    string
	MRIID        int
	Repository   string
	ChangeID     string
	Title        string
	Description  string
	WebURL       string
	SourceSHA    string
	TargetSHA    string
	SourceBranch string
	TargetBranch string
	Author       string
	Draft        bool `json:"Draft,omitempty"`
	Labels       []string
	ChangedPaths []string
}
