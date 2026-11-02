package operator

type Command struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	RequiresRun bool
	Examples    []string
}

var Commands = []Command{
	{Name: "/help", Aliases: []string{"?", "/?"}, Usage: "/help", Description: "Show slash commands and examples."},
	{Name: "/status", Aliases: []string{"/ready"}, Usage: "/status", Description: "Show agent readiness and sidecar status."},
	{Name: "/config", Aliases: []string{"/env"}, Usage: "/config", Description: "Show redacted runtime configuration."},
	{Name: "/providers", Aliases: []string{"/models"}, Usage: "/providers", Description: "Show model providers and role routes."},
	{Name: "/skills", Aliases: []string{"/skill"}, Usage: "/skills", Description: "Show loaded review skills."},
	{Name: "/tools", Aliases: []string{"/tool"}, Usage: "/tools", Description: "Show implemented operator tools."},
	{Name: "/sessions", Aliases: []string{"/runs"}, Usage: "/sessions [status] [limit]", Description: "List review sessions.", Examples: []string{"/sessions drafted 5"}},
	{Name: "/run", Aliases: []string{"/current"}, Usage: "/run", Description: "Show current run summary.", RequiresRun: true},
	{Name: "/history", Aliases: []string{"/events"}, Usage: "/history [type] [limit]", Description: "Show current run timeline.", RequiresRun: true, Examples: []string{"/history chat_message 20"}},
	{Name: "/diff", Aliases: []string{"/changes"}, Usage: "/diff", Description: "Show changed files and patch summary.", RequiresRun: true},
	{Name: "/context", Aliases: []string{"/evidence"}, Usage: "/context", Description: "Show selected review context and graph trace reasons.", RequiresRun: true},
	{Name: "/draft", Aliases: []string{"/report"}, Usage: "/draft [output-file]", Description: "Show or write the current draft report.", RequiresRun: true, Examples: []string{"/draft final.md"}},
	{Name: "/memory", Aliases: []string{"/mempalace"}, Usage: "/memory", Description: "Preview approved MemPalace proposal.", RequiresRun: true},
	{Name: "/approve", Aliases: []string{"/hil"}, Usage: "/approve --report-file <path>", Description: "Approve and publish the final review.", RequiresRun: true, Examples: []string{"/approve --report-file final.md"}},
	{Name: "/publish-final", Aliases: []string{"/publish"}, Usage: "/publish-final --report-file <path>", Description: "Retry final report publishing.", RequiresRun: true, Examples: []string{"/publish-final --report-file final.md"}},
	{Name: "/attempt", Usage: "/attempt <exact-attempt-id>", Description: "Show canonical attempt identity, version and reservation status."},
	{Name: "/cancel-attempt", Usage: "/cancel-attempt <exact-attempt-id> --command-id <id> --expected-version <n> --revision <revision> --reason <text>", Description: "Cancel an exact canonical attempt with a versioned command."},
	{Name: "/authorize-publication", Usage: "/authorize-publication <exact-attempt-id> --command-id <id> --expected-version <n> --revision <revision> --assessment-digest <sha256> --artifact-digest <sha256> --provider <provider> --host <host> --repository-id <id> --change-number <number> --artifact-class <class> --reason <text>", Description: "Authorize disclosure of one exact validated terminal assessment."},
}

func CanonicalCommand(value string) string {
	value = lowerTrim(value)
	for _, command := range Commands {
		if value == lowerTrim(command.Name) {
			return command.Name
		}
		for _, alias := range command.Aliases {
			if value == lowerTrim(alias) {
				return command.Name
			}
		}
	}
	return value
}
