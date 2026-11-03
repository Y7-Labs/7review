package channel

import "strings"

func EvaluateInbound(msg InboundMessage) InboundResult {
	if disposition, ok := parseFindingDispositionCommand(msg); ok {
		return disposition
	}
	command, commandRunID, approved, report, request, findingID := parseCommand(msg.Text, msg.RunID)
	result := InboundResult{
		RunID:       firstNonEmpty(msg.RunID, commandRunID),
		Channel:     strings.TrimSpace(msg.Channel),
		Sender:      firstNonEmpty(msg.SenderID, msg.SenderAddress),
		Command:     command,
		Approved:    approved,
		FinalReport: report,
		Request:     request,
		FindingID:   findingID,
	}
	if result.RunID == "" {
		result.Reason = "missing run id"
		return result
	}
	if !approved {
		result.Reason = "message recorded without explicit approval command"
	}
	switch command {
	case "revise":
		result.Revised = request != ""
		if result.Revised {
			result.Reason = ""
		}
	case "suppress":
		result.Suppressed = findingID != "" && request != ""
		if result.Suppressed {
			result.Reason = ""
		}
	}
	return result
}

func parseFindingDispositionCommand(msg InboundMessage) (InboundResult, bool) {
	text := strings.TrimSpace(msg.Text)
	lines := strings.Split(text, "\n")
	fields := strings.Fields(strings.TrimSpace(lines[0]))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/disposition-finding") {
		return InboundResult{}, false
	}
	result := InboundResult{Channel: strings.TrimSpace(msg.Channel), Sender: firstNonEmpty(msg.SenderID, msg.SenderAddress), Command: "disposition-finding"}
	if len(fields) != 5 {
		result.Reason = "exact attempt, finding, decision and command IDs are required"
		return result, true
	}
	result.AttemptID = strings.TrimSpace(fields[1])
	result.FindingID = strings.TrimSpace(fields[2])
	result.Decision = strings.ToLower(strings.TrimSpace(fields[3]))
	result.CommandID = strings.TrimSpace(fields[4])
	result.Request = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	if result.AttemptID == "" || result.FindingID == "" || result.CommandID == "" || result.Request == "" {
		result.Reason = "exact finding disposition and reason are required"
		return result, true
	}
	switch result.Decision {
	case "accepted", "rejected", "disputed":
		return result, true
	default:
		result.Reason = "invalid finding decision"
		return result, true
	}
}

func parseCommand(text string, runID string) (string, string, bool, string, string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", false, "", "", ""
	}
	lines := strings.Split(text, "\n")
	first := strings.TrimSpace(lines[0])
	fields := strings.Fields(first)
	if len(fields) == 0 {
		return "", "", false, "", "", ""
	}
	command := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if !strings.HasPrefix(fields[0], "/") {
		return command, "", false, "", "", ""
	}
	if command != "approve" && command != "revise" && command != "suppress" {
		return command, "", false, "", "", ""
	}
	commandRunID := ""
	if len(fields) >= 2 {
		commandRunID = strings.TrimSpace(fields[1])
	}
	if commandRunID == "" || (strings.TrimSpace(runID) != "" && commandRunID != strings.TrimSpace(runID)) {
		return command, commandRunID, false, "", "", ""
	}
	body := strings.TrimSpace(strings.Join(lines[1:], "\n"))
	if (command == "suppress" && len(fields) != 3) || (command != "suppress" && len(fields) != 2) {
		return command, commandRunID, false, "", "", ""
	}
	switch command {
	case "approve":
		return command, commandRunID, true, body, "", ""
	case "revise":
		return command, commandRunID, false, "", body, ""
	case "suppress":
		if len(fields) < 3 {
			return command, commandRunID, false, "", body, ""
		}
		return command, commandRunID, false, "", body, strings.TrimSpace(fields[2])
	default:
		return command, commandRunID, false, "", "", ""
	}
}

func RunIDFromCommand(text string) string {
	_, runID, _, _, _, _ := parseCommand(text, "")
	return runID
}
