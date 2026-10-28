package policy

func (limits LimitsV2) Clone() LimitsV2 {
	if limits.Attempt.Period != nil {
		p := *limits.Attempt.Period
		limits.Attempt.Period = &p
	}
	if limits.Change.Period != nil {
		p := *limits.Change.Period
		limits.Change.Period = &p
	}
	if limits.Project.Period != nil {
		p := *limits.Project.Period
		limits.Project.Period = &p
	}
	return limits
}
