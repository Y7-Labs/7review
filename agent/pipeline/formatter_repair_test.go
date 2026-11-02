package pipeline

import "testing"

func TestFormatterToolRequestCannotBecomeSuccessfulEmptyFindingsRepair(t *testing.T) {
	raw := `{"tool_requests":[{"name":"get_changed_files","input":{}}]}`
	_, status := parseFindingsDetailed(raw)
	if findings, ok := parseRepairedFindings(raw, status); ok {
		t.Fatalf("formatter tool request became repaired review: status=%s findings=%#v", status, findings)
	}
}
