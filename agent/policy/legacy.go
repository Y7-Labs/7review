package policy

import "github.com/Y4NN777/7review/agent/profile"

// LegacyPolicy records exactly which version-1 behavior remains active. It is
// intentionally not ReviewConfigV2: missing authority and budget data must not
// be invented during migration.
type LegacyPolicy struct {
	SourceVersion                 int
	Name                          string
	FinalRequiresHumanApproval    bool
	InlineStrengths               []string
	DraftOnlyStrengths            []string
	IgnoredPaths                  []string
	CorpusRoots                   []profile.CorpusRoot
	UnspecifiedV2Obligations      []string
	MigrationRequiresAcknowledged bool
}

func TranslateLegacy(compiled *profile.CompiledProfile) LegacyPolicy {
	if compiled == nil {
		return LegacyPolicy{}
	}
	return LegacyPolicy{
		SourceVersion:              1,
		Name:                       compiled.Name,
		FinalRequiresHumanApproval: compiled.Publishing.FinalRequiresHumanApproval,
		InlineStrengths:            append([]string(nil), compiled.Publishing.InlineStrengths...),
		DraftOnlyStrengths:         append([]string(nil), compiled.Publishing.DraftOnlyStrengths...),
		IgnoredPaths:               append([]string(nil), compiled.PathPolicy.Ignore...),
		CorpusRoots:                append([]profile.CorpusRoot(nil), compiled.Corpus.Roots...),
		UnspecifiedV2Obligations: []string{
			"trusted_snapshot", "resource_limits", "capabilities", "scope_mappings",
			"quality_gate", "retention", "delegations",
		},
		MigrationRequiresAcknowledged: true,
	}
}
