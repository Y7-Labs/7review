package channel

import (
	"fmt"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

func draftBody(title string, msg DraftMessage) string {
	body := fmt.Sprintf("%s\nrun: %s\nrepository: %s\nchange: %s\nurl: %s\nsummary: %s\n\n%s\n\nReply with /approve %s, /revise %s, or /suppress %s <finding_id>.",
		title, msg.RunID, msg.Repository, msg.ChangeID, msg.WebURL, msg.Summary, msg.DraftReport, msg.RunID, msg.RunID, msg.RunID)
	if review.ValidateOperatorAttemptID(msg.AttemptID) != nil {
		return body
	}
	seen := map[string]bool{}
	findings := make([]string, 0, len(msg.FindingIDs))
	for _, findingID := range msg.FindingIDs {
		if review.ValidateOperatorFindingID(findingID) != nil || seen[findingID] {
			continue
		}
		seen[findingID] = true
		findings = append(findings, findingID)
	}
	if len(findings) == 0 {
		return body
	}
	return body + fmt.Sprintf("\n\nCanonical attempt: %s\nCanonical findings: %s\nTo record an attributed decision, reply:\n/disposition-finding %s <finding-id> <accepted|rejected|disputed> <unique-command-id>\n<reason>", msg.AttemptID, strings.Join(findings, ", "), msg.AttemptID)
}

func firstSender(senders []string) string {
	if len(senders) == 0 {
		return ""
	}
	return strings.TrimSpace(senders[0])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func cleanSenders(senders []string) []string {
	out := make([]string, 0, len(senders))
	seen := map[string]bool{}
	for _, sender := range senders {
		sender = strings.TrimSpace(sender)
		if sender == "" {
			continue
		}
		key := strings.ToLower(sender)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sender)
	}
	return out
}

func senderAllowed(allowed []string, msg InboundMessage) bool {
	if len(allowed) == 0 {
		return true
	}
	senderID := strings.TrimSpace(msg.SenderID)
	senderAddress := strings.TrimSpace(msg.SenderAddress)
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.EqualFold(item, senderID) || strings.EqualFold(item, senderAddress) {
			return true
		}
	}
	return false
}
