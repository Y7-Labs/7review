package pipeline

import (
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

// Parse each completed batch independently. Concatenated JSON candidates select
// only the first response and cannot establish coverage of the remaining slots.
func parseReviewResponses(responses []string) ([]review.Finding, []review.SkillCoverage, []review.ToolRequest, string) {
	if len(responses) == 0 {
		return nil, nil, nil, "empty_response"
	}
	var findings []review.Finding
	var coverages [][]review.SkillCoverage
	var requests []review.ToolRequest
	status := "empty_findings"
	for _, raw := range responses {
		f, c, r, s := parseReviewOutputDetailed(raw)
		findings = append(findings, f...)
		coverages = append(coverages, c)
		requests = append(requests, r...)
		if isFatalModelParseStatus(s) {
			status = s
		} else if !isFatalModelParseStatus(status) && len(f) > 0 {
			status = "parsed"
		}
	}
	if isFatalModelParseStatus(status) {
		requests = nil
	}
	if len(requests) > review.MaxModelToolRequests {
		requests = nil
		status = "tool_loop_incomplete"
	}
	return findings, combineBatchCoverage(coverages), requests, status
}

func combineBatchCoverage(batches [][]review.SkillCoverage) []review.SkillCoverage {
	if len(batches) == 1 {
		return batches[0]
	}
	var out []review.SkillCoverage
	indices := map[string]int{}
	counts := map[string]int{}
	incomplete := map[string]bool{}
	for _, items := range batches {
		seen := map[string]bool{}
		for _, item := range items {
			key := strings.ToLower(strings.TrimSpace(item.Name))
			if key == "" {
				continue
			}
			if seen[key] {
				incomplete[key] = true
				continue
			}
			seen[key] = true
			counts[key]++
			if !skillCoverageIsMeaningful(item) {
				incomplete[key] = true
			}
			index, exists := indices[key]
			if !exists {
				indices[key] = len(out)
				out = append(out, item)
				continue
			}
			current := &out[index]
			current.Evidence = compactStrings(append(current.Evidence, item.Evidence...))
			current.Tools = compactStrings(append(current.Tools, item.Tools...))
			current.Checks = compactStrings(append(current.Checks, item.Checks...))
			if item.Notes != "" {
				current.Notes += "\n" + item.Notes
			}
		}
	}
	for key, index := range indices {
		if incomplete[key] || counts[key] != len(batches) {
			out[index].Status = "incomplete_batch_coverage"
		}
	}
	return out
}
