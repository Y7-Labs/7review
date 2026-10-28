package channel

import "strings"

func EvaluateInbound(msg InboundMessage) InboundResult {
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
