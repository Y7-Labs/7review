package review

// Clone returns a detached snapshot suitable for persistence and read models.
func (s Source) Clone() Source {
	out := s
	out.Request.Labels = cloneStrings(s.Request.Labels)
	out.Request.ChangedPaths = cloneStrings(s.Request.ChangedPaths)
	out.SCM = cloneSCMContext(s.SCM)
	out.ChangedFiles = append([]ChangedFile(nil), s.ChangedFiles...)
	if s.Diff != nil {
		diff := *s.Diff
		diff.Files = append([]FileDiff(nil), s.Diff.Files...)
		out.Diff = &diff
	}
	out.CorpusSections = append([]Section(nil), s.CorpusSections...)
	out.Evidence = cloneEvidence(s.Evidence)
	out.SkillSections = append([]Section(nil), s.SkillSections...)
	out.SkillActivations = cloneSkillActivations(s.SkillActivations)
	out.Memory = MemoryRecall{
		Conventions: cloneStrings(s.Memory.Conventions),
		Decisions:   cloneStrings(s.Memory.Decisions),
		History:     cloneStrings(s.Memory.History),
	}
	out.Model.RawResponses = cloneStrings(s.Model.RawResponses)
	out.SkillCoverage = cloneSkillCoverage(s.SkillCoverage)
	out.ToolRequests = cloneToolRequests(s.ToolRequests)
	out.ToolObservations = append([]ToolObservation(nil), s.ToolObservations...)
	out.Findings = cloneFindings(s.Findings)
	out.HumanCheck = cloneFindings(s.HumanCheck)
	out.Notes = cloneFindings(s.Notes)
	out.Questions = cloneFindings(s.Questions)
	out.InlineComments = append([]InlineComment(nil), s.InlineComments...)
	out.Investigation = cloneInvestigation(s.Investigation)
	out.Coverage = cloneCoverage(s.Coverage)
	out.Assessment.FindingIDs = cloneStrings(s.Assessment.FindingIDs)
	out.Assessment.EvidenceRefs = cloneStrings(s.Assessment.EvidenceRefs)
	out.Gate = s.Gate.Clone()
	out.Delivery = s.Delivery.Clone()
	out.Readiness.IntentSummary.Provenance = append([]string(nil), s.Readiness.IntentSummary.Provenance...)
	out.Readiness.AcceptanceCriteria.Provenance = append([]string(nil), s.Readiness.AcceptanceCriteria.Provenance...)
	out.Readiness.TestEvidence.Provenance = append([]string(nil), s.Readiness.TestEvidence.Provenance...)
	out.Run.StepProviders = cloneStringMap(s.Run.StepProviders)
	out.Run.AvailableTools = cloneStrings(s.Run.AvailableTools)
	out.Run.Warnings = cloneStrings(s.Run.Warnings)
	return out
}

func cloneSCMContext(in *SCMContext) *SCMContext {
	if in == nil {
		return nil
	}
	out := *in
	out.Labels = cloneStrings(in.Labels)
	out.Commits = append([]Commit(nil), in.Commits...)
	out.Files = append([]ChangedFile(nil), in.Files...)
	out.Discussions = append([]Discussion(nil), in.Discussions...)
	out.Checks = append([]CheckRun(nil), in.Checks...)
	out.Approvals = append([]Approval(nil), in.Approvals...)
	return &out
}

func cloneEvidence(in []EvidenceItem) []EvidenceItem {
	out := append([]EvidenceItem(nil), in...)
	for i := range out {
		out[i].MatchedSignals = cloneStrings(in[i].MatchedSignals)
	}
	return out
}

func cloneSkillActivations(in []SkillActivation) []SkillActivation {
	out := append([]SkillActivation(nil), in...)
	for i := range out {
		out[i].AllowedTools = cloneStrings(in[i].AllowedTools)
		out[i].RequiredChecks = cloneStrings(in[i].RequiredChecks)
	}
	return out
}

func cloneSkillCoverage(in []SkillCoverage) []SkillCoverage {
	out := append([]SkillCoverage(nil), in...)
	for i := range out {
		out[i].Evidence = cloneStrings(in[i].Evidence)
		out[i].Tools = cloneStrings(in[i].Tools)
		out[i].Checks = cloneStrings(in[i].Checks)
	}
	return out
}

func cloneToolRequests(in []ToolRequest) []ToolRequest {
	out := append([]ToolRequest(nil), in...)
	for i := range out {
		if in[i].Input == nil {
			continue
		}
		out[i].Input = make(map[string]any, len(in[i].Input))
		for key, value := range in[i].Input {
			out[i].Input[key] = cloneValue(value)
		}
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return cloneStrings(typed)
	default:
		return value
	}
}

func cloneFindings(in []Finding) []Finding {
	out := append([]Finding(nil), in...)
	for i := range out {
		out[i].Citations = append([]EvidenceCitation(nil), in[i].Citations...)
	}
	return out
}

func cloneInvestigation(in InvestigationProjection) InvestigationProjection {
	out := in
	out.Events = append([]AttemptEvent(nil), in.Events...)
	for i := range out.Events {
		out.Events[i].Metadata = cloneStringMap(in.Events[i].Metadata)
	}
	out.Decisions = append([]ExecutionDecision(nil), in.Decisions...)
	for i := range out.Decisions {
		out.Decisions[i].InputArtifactRefs = cloneStrings(in.Decisions[i].InputArtifactRefs)
		out.Decisions[i].CheckRefs = cloneStrings(in.Decisions[i].CheckRefs)
	}
	out.HumanDecisions = append([]HumanDecision(nil), in.HumanDecisions...)
	out.Observations = append([]Observation(nil), in.Observations...)
	for i := range out.Observations {
		out.Observations[i].SourceRefs = cloneStrings(in.Observations[i].SourceRefs)
	}
	return out
}

func cloneCoverage(in CoverageProjection) CoverageProjection {
	out := in
	out.Checks = append([]Check(nil), in.Checks...)
	for i := range out.Checks {
		out.Checks[i].EvidenceRefs = cloneStrings(in.Checks[i].EvidenceRefs)
	}
	out.UnknownCheckIDs = cloneStrings(in.UnknownCheckIDs)
	return out
}

func cloneStrings(in []string) []string {
	return append([]string(nil), in...)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
