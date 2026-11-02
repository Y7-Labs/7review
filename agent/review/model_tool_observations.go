package review

const (
	MaxModelToolRequests            = 5
	MaxModelToolNameBytes           = 128
	MaxModelToolReasonBytes         = 1024
	MaxModelToolInputBytes          = 4096
	MaxSnapshotToolResultBytes      = 4096
	MaxModelToolObservationBytes    = 12 << 10
	MaxModelToolObservationSetBytes = 128 << 10
)

// ModelToolObservations is a local snapshot rendering, not a dispatch grant,
// financial receipt, validated assessment, approval or publication.
type ModelToolObservations struct {
	Version                                             int
	RunID, AttemptID                                    string
	ParentGroupDigest, ParentInputDigest, RequestDigest string
	Requests                                            []ToolRequest
	Observations                                        []ToolObservation
}
