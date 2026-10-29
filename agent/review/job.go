package review

import "context"

// JobClaim is host-issued execution authority, never a field in webhook input.
type JobClaim struct{ RunID, Token string }
type jobClaimKey struct{}

func WithJobClaim(ctx context.Context, claim JobClaim) context.Context {
	return context.WithValue(ctx, jobClaimKey{}, claim)
}
func JobClaimFromContext(ctx context.Context) *JobClaim {
	claim, ok := ctx.Value(jobClaimKey{}).(JobClaim)
	if !ok {
		return nil
	}
	return &claim
}
