package policy

import "fmt"

// Only obligations are inherited. Selection, preference and delegation remain
// properties of the declaring pack, never authority acquired from an ancestor.
func expandPacks(packs []MethodPackV2) ([]MethodPackV2, error) {
	byID := make(map[string]MethodPackV2, len(packs))
	for _, pack := range packs {
		byID[pack.ID] = pack
	}
	state := map[string]int{}
	memo := map[string]MethodPackV2{}
	var visit func(string) (MethodPackV2, error)
	visit = func(id string) (MethodPackV2, error) {
		pack, ok := byID[id]
		if !ok {
			return MethodPackV2{}, fmt.Errorf("policy: unknown parent pack %q", id)
		}
		if state[id] == 1 {
			return MethodPackV2{}, fmt.Errorf("policy: inheritance cycle at pack %q", id)
		}
		if state[id] == 2 {
			return memo[id], nil
		}
		state[id] = 1
		expanded := pack
		expanded.Methods, expanded.Checks = nil, nil
		expanded.RiskFloor, expanded.IndependentReview = "none", false
		expanded.Publication = PublicationConstraintV2{Mode: "inherit", ArtifactClasses: []string{"assessment", "comment", "check", "annotation", "code_quality"}}
		merge := func(parent MethodPackV2) {
			expanded.Methods = stableUnion(expanded.Methods, parent.Methods)
			expanded.Checks = stableUnion(expanded.Checks, parent.Checks)
			if riskRank(parent.RiskFloor) > riskRank(expanded.RiskFloor) {
				expanded.RiskFloor = parent.RiskFloor
			}
			expanded.IndependentReview = expanded.IndependentReview || parent.IndependentReview
			expanded.Publication = restrictivePublication(expanded.Publication, parent.Publication)
		}
		for _, parentID := range sortedCopy(pack.Inherits) {
			parent, err := visit(parentID)
			if err != nil {
				return MethodPackV2{}, err
			}
			merge(parent)
		}
		merge(pack)
		if len(expanded.Methods) > 32 || len(expanded.Checks) > 128 {
			return MethodPackV2{}, fmt.Errorf("policy: expanded pack %q exceeds method/check bounds", id)
		}
		state[id], memo[id] = 2, expanded
		return expanded, nil
	}
	out := make([]MethodPackV2, 0, len(packs))
	for _, pack := range packs {
		expanded, err := visit(pack.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}
