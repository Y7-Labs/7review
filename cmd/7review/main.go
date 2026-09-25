package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/app"
	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/llm/providers"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
	"github.com/Y4NN777/7review/agent/ui"
)

const operatorRequestTimeout = 60 * time.Second
const operatorStreamTimeout = 10 * time.Minute
const maxSSEEventBytes = 4 << 20

func main() {
	if len(os.Args) > 1 && os.Args[1] == "policy" {
		if err := runPolicyCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "status" {
		if err := runStatus(os.Args[2:], os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "chat" {
		runChat()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		if err := runTUI(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "runs" {
		runListRuns()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "review" {
		if err := runRequestReview(os.Args[2:], os.Stdout, operatorRequestHTTPClient()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sessions" {
		if err := runSessions(os.Args[2:], os.Stdout, operatorRequestHTTPClient()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "session" {
		if err := runSession(os.Args[2:], os.Stdout, operatorRequestHTTPClient()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		runGetRun()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "history" {
		runHistory()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "approve" {
		runApprove()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "publish-final" {
		runPublishFinal()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(os.Args[2:], operatorRequestHTTPClient()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	server, err := app.NewServer()
	if err != nil {
		log.Fatal(err)
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func runListRuns() {
	serverURL := commandServerURL(os.Args[2:])
	body, err := requestAgent(operatorRequestHTTPClient(), http.MethodGet, strings.TrimRight(serverURL, "/")+"/runs", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(body)
}

type requestReviewCommandResult struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func runRequestReview(args []string, out io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: 7review review gitlab --project <project-id> --mr <iid> [--server <url>]\n       7review review github --repo <owner/repo> --pr <number> [--server <url>]")
	}
	provider := strings.ToLower(strings.TrimSpace(args[0]))
	opts := map[string]any{"provider": provider}
	serverURL := "http://localhost:8080"
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch flagName(arg) {
		case "--server":
			serverURL = flagValue(args, &i)
		case "--project", "--project-id":
			opts["project_id"] = flagValue(args, &i)
		case "--mr", "--mr-iid":
			opts["mr_iid"] = parsePositiveInt(flagValue(args, &i))
		case "--repo", "--repository":
			opts["repository"] = flagValue(args, &i)
		case "--pr", "--pr-number":
			opts["pr_number"] = parsePositiveInt(flagValue(args, &i))
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown review flag %s", arg)
			}
		}
	}
	var result requestReviewCommandResult
	if err := executeRemoteTool(client, serverURL, "request_review", opts, &result); err != nil {
		return err
	}
	if result.RunID == "" {
		return fmt.Errorf("request_review returned no run id")
	}
	if result.Reason != "" {
		fmt.Fprintf(out, "review %s for %s: %s\n", result.Status, result.RunID, result.Reason)
		return nil
	}
	fmt.Fprintf(out, "review %s for %s\n", result.Status, result.RunID)
	return nil
}

func runSessions(args []string, out io.Writer, client *http.Client) error {
	opts := parseSessionsArgs(args)
	var runs []remoteRunRow
	if err := executeRemoteTool(client, opts.serverURL, "list_runs", nil, &runs); err != nil {
		return err
	}
	fmt.Fprintln(out, renderSessionsSummary(runs, opts))
	return nil
}

func runSession(args []string, out io.Writer, client *http.Client) error {
	opts := parseHistoryArgs(args)
	if opts.runID == "" {
		return fmt.Errorf("missing session run id")
	}
	detail, err := fetchRemoteRunDetail(client, opts.serverURL, opts.runID)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, renderSessionDetail(detail, opts))
	return nil
}

type sessionsCommandOptions struct {
	serverURL string
	status    string
	provider  string
	query     string
	limit     int
}

func parseSessionsArgs(args []string) sessionsCommandOptions {
	opts := sessionsCommandOptions{serverURL: "http://localhost:8080"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch flagName(arg) {
		case "--server":
			opts.serverURL = flagValue(args, &i)
		case "--status":
			opts.status = flagValue(args, &i)
		case "--provider":
			opts.provider = flagValue(args, &i)
		case "--query", "--search":
			opts.query = flagValue(args, &i)
		case "--limit":
			opts.limit = parsePositiveInt(flagValue(args, &i))
		default:
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if opts.status == "" && isKnownSessionStatus(arg) {
				opts.status = arg
				continue
			}
			if opts.limit == 0 {
				if limit := parsePositiveInt(arg); limit > 0 {
					opts.limit = limit
					continue
				}
			}
			if opts.query == "" {
				opts.query = arg
			}
		}
	}
	return opts
}

func runGetRun() {
	serverURL := "http://localhost:8080"
	runID := ""
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch flagName(arg) {
		case "--server":
			serverURL = flagValue(args, &i)
		case "--run":
			runID = flagValue(args, &i)
		default:
			if !strings.HasPrefix(arg, "-") && runID == "" {
				runID = arg
			}
		}
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "missing run id")
		os.Exit(1)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/run?id=" + url.QueryEscape(runID)
	body, err := requestAgent(operatorRequestHTTPClient(), http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(body)
}

func runApprove() {
	opts, err := parseApprovalArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := submitApproval(operatorRequestHTTPClient(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("approval queued for %s\n", opts.approvalTarget())
}

func runPublishFinal() {
	opts, err := parsePublishArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := submitFinalPublish(operatorRequestHTTPClient(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("final publish queued for %s\n", opts.runID)
}

func runSetup() {
	opts := ui.SetupOptions{OutputPath: ".env"}
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--force":
			opts.Force = true
		case "--plain":
			opts.Plain = true
		default:
			opts.OutputPath = arg
		}
	}
	if err := ui.RunSetupWizard(os.Stdin, os.Stdout, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runChat() {
	opts, serverURL, runID := parseChatArgs(os.Args[2:])
	opts.CommandHandler = chatCommandHandler(serverURL, runID)
	chatCtx := ui.ChatContext{ServerURL: serverURL}
	var responder ui.ChatResponder
	if runID != "" {
		chatCtx.ConfigLoaded = true
		chatCtx.RunID = runID
		responder = &remoteRunChatResponder{
			serverURL:  serverURL,
			runID:      runID,
			httpClient: operatorStreamHTTPClient(),
		}
	} else {
		cfg, err := config.LoadConfig()
		if err != nil {
			chatCtx.ConfigError = err.Error()
			responder = ui.StaticResponder{Message: "Config is not ready. Run 7review setup, then 7review status. Missing config: " + err.Error()}
		} else {
			chatCtx.ConfigLoaded = true
			chatCtx.HeadroomURL = cfg.HeadroomURL
			chatCtx.MemPalaceURL = cfg.MemPalaceURL
			chatCtx.EmbeddingModel = cfg.EmbeddingModel
			modelOrchestrator, err := orchestrator.BuildOrchestrator(cfg)
			if err != nil {
				chatCtx.ConfigLoaded = false
				chatCtx.ConfigError = err.Error()
				responder = ui.StaticResponder{Message: "Model orchestration is not ready: " + err.Error()}
			} else {
				responder = &modelChatResponder{
					orchestrator: modelOrchestrator,
					cfg:          cfg,
				}
			}
		}
	}
	if err := ui.RunChat(context.Background(), os.Stdin, os.Stdout, chatCtx, responder, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func chatCommandHandler(serverURL, runID string) ui.ChatCommandFunc {
	return chatCommandHandlerWithClient(serverURL, runID, operatorRequestHTTPClient())
}

func parseChatCommandFields(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var fields []string
	var b strings.Builder
	var quote rune
	escaped := false
	inField := false
	for _, r := range text {
		if escaped {
			b.WriteRune(r)
			escaped = false
			inField = true
			continue
		}
		if r == '\\' {
			escaped = true
			inField = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			inField = true
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			inField = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if inField {
				fields = append(fields, b.String())
				b.Reset()
				inField = false
			}
			continue
		}
		b.WriteRune(r)
		inField = true
	}
	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in chat command")
	}
	if inField {
		fields = append(fields, b.String())
	}
	return fields, nil
}

func chatCommandHelp(hasRun bool) string {
	lines := make([]string, 0, len(slashCommands)+1)
	for _, command := range slashCommands {
		if command.RequiresRun && !hasRun {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-14s %s", command.Name, command.Description))
		for _, example := range command.Examples {
			lines = append(lines, fmt.Sprintf("%-14s example", example))
		}
	}
	lines = append(lines, "quit           exit chat")
	return strings.Join(lines, "\n")
}

func renderMemoryProposalSummary(status remoteMemoryProposalStatus) string {
	lines := []string{
		"memory " + status.Run,
		fmt.Sprintf("approved %t", status.Approved),
		fmt.Sprintf("final_bytes %d", status.FinalBytes),
		fmt.Sprintf("conventions %d decisions %d vectors %d", len(status.Proposal.Conventions), len(status.Proposal.Decisions), len(status.Proposal.Vectors)),
	}
	for _, convention := range firstStrings(status.Proposal.Conventions, 3) {
		lines = append(lines, "convention "+trimCommandLine(convention, 96))
	}
	for _, decision := range firstStrings(status.Proposal.Decisions, 3) {
		lines = append(lines, "decision "+trimCommandLine(decision, 96))
	}
	for _, vector := range firstVectors(status.Proposal.Vectors, 3) {
		label := strings.TrimSpace(vector.ID)
		if label == "" {
			label = trimCommandLine(vector.Text, 48)
		}
		lines = append(lines, "vector "+label)
	}
	return strings.Join(lines, "\n")
}

func firstStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func firstVectors(values []remoteVector, limit int) []remoteVector {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func trimCommandLine(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func renderConfigStatusSummary(status remoteConfigStatus) string {
	lines := []string{"config"}
	lines = appendIfSet(lines, "listen   ", status.ListenAddr)
	lines = appendIfSet(lines, "provider ", status.Provider)
	lines = appendIfSet(lines, "review   ", status.ReviewModel)
	lines = appendIfSet(lines, "small    ", status.SmallModel)
	lines = appendIfSet(lines, "embed    ", status.EmbeddingModel)
	lines = appendIfSet(lines, "orch     ", status.Orchestrator)
	lines = appendIfSet(lines, "corpus   ", status.CorpusRoot)
	if status.MaxSupportingCorpusSections > 0 {
		lines = append(lines, fmt.Sprintf("support  %d", status.MaxSupportingCorpusSections))
	}
	lines = appendIfSet(lines, "memory   ", status.MemoryDir)
	lines = appendIfSet(lines, "hil      ", status.HILChannel)
	lines = appendIfSet(lines, "headroom ", status.HeadroomURL)
	lines = appendIfSet(lines, "mempalace", status.MemPalaceURL)
	if status.WebhookWorkers > 0 || status.WebhookQueueSize > 0 {
		lines = append(lines, fmt.Sprintf("workers  %d queue=%d", status.WebhookWorkers, status.WebhookQueueSize))
	}
	lines = append(lines, "integrations")
	lines = append(lines,
		fmt.Sprintf("github=%t gitlab=%t", status.HasGitHub, status.HasGitLab),
		fmt.Sprintf("openai=%t openrouter=%t deepseek=%t", status.HasOpenAI, status.HasOpenRouter, status.HasDeepSeek),
		fmt.Sprintf("anthropic=%t mistral=%t gemini=%t ollama=%t", status.HasAnthropic, status.HasMistral, status.HasGemini, status.HasOllama),
	)
	return strings.Join(lines, "\n")
}

func appendIfSet(lines []string, label string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return lines
	}
	return append(lines, label+" "+strings.TrimSpace(value))
}

func renderSessionsSummary(runs []remoteRunRow, opts sessionsCommandOptions) string {
	filtered := filterSessionRows(runs, opts)
	limit := opts.limit
	if limit <= 0 {
		limit = 12
	}
	header := fmt.Sprintf("sessions %d", len(filtered))
	if len(filtered) != len(runs) {
		header = fmt.Sprintf("sessions %d/%d", len(filtered), len(runs))
	}
	var filters []string
	if opts.status != "" {
		filters = append(filters, "status="+opts.status)
	}
	if opts.provider != "" {
		filters = append(filters, "provider="+opts.provider)
	}
	if opts.query != "" {
		filters = append(filters, "query="+opts.query)
	}
	if opts.limit > 0 {
		filters = append(filters, fmt.Sprintf("limit=%d", opts.limit))
	}
	if len(filters) > 0 {
		header += " " + strings.Join(filters, " ")
	}
	if len(filtered) == 0 {
		return header
	}
	sorted := append([]remoteRunRow(nil), filtered...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].UpdatedAt.Equal(sorted[j].UpdatedAt) {
			return sorted[i].ID < sorted[j].ID
		}
		if sorted[i].UpdatedAt.IsZero() {
			return false
		}
		if sorted[j].UpdatedAt.IsZero() {
			return true
		}
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})
	lines := []string{header}
	for _, run := range firstRunRows(sorted, limit) {
		status := strings.TrimSpace(run.Status)
		if status == "" {
			status = "unknown"
		}
		if run.HILApproved {
			status += "+approved"
		}
		updated := "-"
		if !run.UpdatedAt.IsZero() {
			updated = run.UpdatedAt.UTC().Format("2006-01-02 15:04Z")
		}
		history := ""
		if run.EventCount > 0 {
			history = fmt.Sprintf(" history=%d", run.EventCount)
		}
		change := strings.TrimSpace(run.ProjectID)
		if run.ChangeID != "" {
			if change == "" {
				change = strings.TrimSpace(run.ChangeID)
			} else {
				change = strings.TrimSpace(change + "!" + run.ChangeID)
			}
		}
		if change == "" {
			change = "-"
		}
		title := trimCommandLine(firstNonEmptyCommand(run.Title, run.ID), 42)
		lines = append(lines, fmt.Sprintf("%-20s %-8s %-18s %-20s%s %s", trimCommandLine(run.ID, 20), trimCommandLine(run.Provider, 8), trimCommandLine(status, 18), updated, history, title))
		lines = append(lines, "  change "+trimCommandLine(change, 96))
	}
	if len(sorted) > limit {
		lines = append(lines, fmt.Sprintf("%d more sessions", len(sorted)-limit))
	}
	return strings.Join(lines, "\n")
}

func filterSessionRows(runs []remoteRunRow, opts sessionsCommandOptions) []remoteRunRow {
	status := strings.ToLower(strings.TrimSpace(opts.status))
	provider := strings.ToLower(strings.TrimSpace(opts.provider))
	query := strings.ToLower(strings.TrimSpace(opts.query))
	if status == "" && provider == "" && query == "" {
		return runs
	}
	out := make([]remoteRunRow, 0, len(runs))
	for _, run := range runs {
		if status != "" && strings.ToLower(strings.TrimSpace(run.Status)) != status {
			continue
		}
		if provider != "" && strings.ToLower(strings.TrimSpace(run.Provider)) != provider {
			continue
		}
		if query != "" && !sessionRowMatchesQuery(run, query) {
			continue
		}
		out = append(out, run)
	}
	return out
}

func sessionRowMatchesQuery(run remoteRunRow, query string) bool {
	values := []string{
		run.ID,
		run.Provider,
		run.ProjectID,
		run.ChangeID,
		run.Title,
		run.Status,
		run.WebURL,
	}
	if run.ProjectID != "" && run.ChangeID != "" {
		values = append(values, run.ProjectID+"!"+run.ChangeID)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), query) {
			return true
		}
	}
	return false
}

func isKnownSessionStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "queued", "running", "drafted", "awaiting_hil", "approved", "published", "finalized", "failed", "ignored":
		return true
	default:
		return false
	}
}

func firstRunRows(values []remoteRunRow, limit int) []remoteRunRow {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func firstNonEmptyCommand(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func renderSkillStatusSummary(skills []remoteSkillStatus) string {
	if len(skills) == 0 {
		return "skills 0"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("skills %d", len(skills)))
	for _, skill := range skills {
		state := "off"
		if skill.Loaded {
			state = "loaded"
		}
		line := fmt.Sprintf("%-28s %s", skill.Name, state)
		if skill.Path != "" {
			line += " " + skill.Path
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderProviderStatusSummary(status remoteProviderStatus) string {
	var lines []string
	lines = append(lines, "providers")
	if status.Mode != "" {
		lines = append(lines, "mode     "+status.Mode)
	}
	if status.ActiveProvider != "" {
		lines = append(lines, "active   "+status.ActiveProvider)
	}
	if len(status.Providers) > 0 {
		lines = append(lines, "configured")
		for _, provider := range status.Providers {
			state := "missing"
			if provider.Configured {
				state = "configured"
			}
			line := fmt.Sprintf("%-14s %s", provider.Name, state)
			if provider.Reason != "" {
				line += " " + provider.Reason
			}
			lines = append(lines, line)
		}
	}
	if len(status.Roles) > 0 {
		lines = append(lines, "roles")
		for _, role := range status.Roles {
			line := fmt.Sprintf("%-10s %s", role.Role, role.Primary)
			if len(role.Fallbacks) > 0 {
				line += " fallback=" + strings.Join(role.Fallbacks, ",")
			}
			if role.Parallel {
				line += fmt.Sprintf(" parallel=%d", role.MaxParallel)
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func renderToolCatalogSummary(catalog []ui.ToolRow) string {
	if len(catalog) == 0 {
		return "tools 0"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("tools %d", len(catalog)))
	for _, tool := range catalog {
		state := "implemented"
		if !tool.Implemented {
			state = "missing"
		}
		flags := []string{state}
		if tool.RequiresApproval {
			flags = append(flags, "approval")
		}
		lines = append(lines, fmt.Sprintf("%-22s %-10s %s", tool.Name, tool.LifecycleStage, strings.Join(flags, " ")))
	}
	return strings.Join(lines, "\n")
}

func renderOrWriteDraft(run remoteRunDetail, args []string) (string, error) {
	draft := strings.TrimSpace(run.DraftReport)
	if draft == "" {
		return "", fmt.Errorf("run %s has no draft report", run.ID)
	}
	if len(args) == 0 {
		return draft, nil
	}
	if len(args) > 1 {
		return "", fmt.Errorf("/draft accepts at most one output path")
	}
	path := strings.TrimSpace(args[0])
	if path == "" {
		return "", fmt.Errorf("/draft output path is empty")
	}
	if err := os.WriteFile(path, []byte(draft), 0o600); err != nil {
		return "", fmt.Errorf("write draft report: %w", err)
	}
	return fmt.Sprintf("draft report written to %s (%d bytes)", path, len(draft)), nil
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if flagName(arg) == name {
			return true
		}
	}
	return false
}

func parseChatArgs(args []string) (ui.ChatOptions, string, string) {
	opts := ui.ChatOptions{}
	serverURL := "http://localhost:8080"
	runID := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch flagName(arg) {
		case "--plain":
			opts.Plain = true
		case "--server":
			serverURL = flagValue(args, &i)
		case "--run":
			runID = flagValue(args, &i)
		default:
			if !strings.HasPrefix(arg, "-") && runID == "" {
				runID = arg
			}
		}
	}
	serverURL = strings.TrimRight(serverURL, "/")
	return opts, serverURL, runID
}

type remoteRunChatResponder struct {
	serverURL  string
	runID      string
	httpClient *http.Client
}

func (r *remoteRunChatResponder) StreamRespond(ctx context.Context, input string, emit func(string) error) error {
	if r.httpClient == nil {
		r.httpClient = operatorStreamHTTPClient()
	}
	payload, _ := json.Marshal(map[string]string{"message": input})
	endpoint := r.serverURL + "/chat/stream"
	if strings.TrimSpace(r.runID) != "" {
		endpoint += "?run=" + url.QueryEscape(r.runID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	addAgentAuthHeaders(req)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server chat failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return readSSE(resp.Body, emit)
}

func readSSE(body io.Reader, emit func(string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "{}" {
			continue
		}
		var event struct {
			Delta string `json:"delta"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		if event.Error != "" {
			return errors.New(event.Error)
		}
		if event.Delta != "" {
			if err := emit(event.Delta); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}
	return nil
}

type approvalCommandOptions struct {
	serverURL string
	runID     string
	projectID string
	mrIID     string
	report    string
}

func (o approvalCommandOptions) approvalTarget() string {
	if o.runID != "" {
		return o.runID
	}
	return o.projectID + "!" + o.mrIID
}

type publishCommandOptions struct {
	serverURL string
	runID     string
	report    string
}

func parseApprovalArgs(args []string) (approvalCommandOptions, error) {
	opts := approvalCommandOptions{serverURL: "http://localhost:8080"}
	var reportFile string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case flagName(arg) == "--server":
			opts.serverURL = flagValue(args, &i)
		case flagName(arg) == "--run":
			opts.runID = flagValue(args, &i)
		case flagName(arg) == "--project":
			opts.projectID = flagValue(args, &i)
		case flagName(arg) == "--mr":
			opts.mrIID = flagValue(args, &i)
		case flagName(arg) == "--report-file":
			reportFile = flagValue(args, &i)
		case flagName(arg) == "--report":
			opts.report = flagValue(args, &i)
		case !strings.HasPrefix(arg, "-") && opts.runID == "":
			opts.runID = arg
		}
	}
	if opts.runID == "" && (opts.projectID == "" || opts.mrIID == "") {
		return opts, errors.New("approve requires --run=<id> or --project=<id> and --mr=<iid>")
	}
	if reportFile != "" {
		report, err := os.ReadFile(reportFile)
		if err != nil {
			return opts, fmt.Errorf("read report file: %w", err)
		}
		opts.report = string(report)
	}
	return opts, nil
}

func parsePublishArgs(args []string) (publishCommandOptions, error) {
	opts := publishCommandOptions{serverURL: "http://localhost:8080"}
	var reportFile string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case flagName(arg) == "--server":
			opts.serverURL = flagValue(args, &i)
		case flagName(arg) == "--run":
			opts.runID = flagValue(args, &i)
		case flagName(arg) == "--report-file":
			reportFile = flagValue(args, &i)
		case flagName(arg) == "--report":
			opts.report = flagValue(args, &i)
		case !strings.HasPrefix(arg, "-") && opts.runID == "":
			opts.runID = arg
		}
	}
	if opts.runID == "" {
		return opts, errors.New("publish-final requires --run=<id> or a positional run id")
	}
	if reportFile != "" {
		report, err := os.ReadFile(reportFile)
		if err != nil {
			return opts, fmt.Errorf("read report file: %w", err)
		}
		opts.report = string(report)
	}
	return opts, nil
}

func submitApproval(client *http.Client, opts approvalCommandOptions) error {
	endpoint := strings.TrimRight(opts.serverURL, "/") + "/approve?"
	if opts.runID != "" {
		endpoint += "run=" + url.QueryEscape(opts.runID)
	} else {
		endpoint += "project=" + url.QueryEscape(opts.projectID) + "&mr=" + url.QueryEscape(opts.mrIID)
	}
	_, err := requestAgent(client, http.MethodPost, endpoint, strings.NewReader(opts.report))
	return err
}

func submitFinalPublish(client *http.Client, opts publishCommandOptions) error {
	endpoint := strings.TrimRight(opts.serverURL, "/") + "/publish/final?run=" + url.QueryEscape(opts.runID)
	_, err := requestAgent(client, http.MethodPost, endpoint, strings.NewReader(opts.report))
	return err
}

func commandServerURL(args []string) string {
	for i := 0; i < len(args); i++ {
		if flagName(args[i]) == "--server" {
			return flagValue(args, &i)
		}
	}
	return "http://localhost:8080"
}

func runHealthcheck(args []string, client *http.Client) error {
	endpoint := "http://127.0.0.1:8080/health"
	for i := 0; i < len(args); i++ {
		switch flagName(args[i]) {
		case "--url":
			endpoint = flagValue(args, &i)
		case "--server":
			endpoint = strings.TrimRight(flagValue(args, &i), "/") + "/health"
		}
	}
	_, err := requestAgent(client, http.MethodGet, endpoint, nil)
	return err
}

func flagName(arg string) string {
	if name, _, ok := strings.Cut(arg, "="); ok {
		return name
	}
	return arg
}

func flagValue(args []string, idx *int) string {
	arg := args[*idx]
	if _, value, ok := strings.Cut(arg, "="); ok {
		return value
	}
	if *idx+1 >= len(args) {
		return ""
	}
	(*idx)++
	return args[*idx]
}

func requestAgent(client *http.Client, method string, endpoint string, body io.Reader) (string, error) {
	_, out, err := requestAgentRaw(client, method, endpoint, body)
	return out, err
}

func requestAgentRaw(client *http.Client, method string, endpoint string, body io.Reader) (int, string, error) {
	if client == nil {
		client = operatorRequestHTTPClient()
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	addAgentAuthHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := strings.TrimSpace(string(data))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, out, fmt.Errorf("%s %s failed: %s: %s", method, endpoint, resp.Status, out)
	}
	return resp.StatusCode, out, nil
}

func operatorRequestHTTPClient() *http.Client {
	return &http.Client{Timeout: operatorRequestTimeout}
}

func operatorStreamHTTPClient() *http.Client {
	return &http.Client{Timeout: operatorStreamTimeout}
}

func addAgentAuthHeaders(req *http.Request) {
	if req == nil {
		return
	}
	if token := os.Getenv("REVIEW_API_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-7review-Token", token)
	}
}

type modelChatResponder struct {
	orchestrator *orchestrator.Orchestrator
	cfg          *config.Config
	memory       operatorMemoryRecall
	history      []string
}

type operatorMemoryRecall interface {
	Recall(context.Context, review.Request) (review.MemoryRecall, error)
}

func (r *modelChatResponder) StreamRespond(ctx context.Context, input string, emit func(string) error) error {
	if answer, ok := deterministicOperatorAnswer(r.cfg, input); ok {
		r.history = append(r.history, "user: "+input, "agent: "+answer)
		if len(r.history) > 12 {
			r.history = r.history[len(r.history)-12:]
		}
		return emit(answer)
	}

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	userMessage := r.userMessage(input, r.operatorMemoryBlock(callCtx, input))
	rc := review.NewContext(review.Request{Provider: "cli", ProjectID: "local", ChangeID: "chat"})
	out, err := r.orchestrator.StreamComplete(callCtx, rc, orchestrator.RoleFormatter, "chat", chatSystemPrompt(r.cfg), userMessage, emit)
	if err != nil {
		return err
	}
	r.history = append(r.history, "user: "+input, "agent: "+out)
	if len(r.history) > 12 {
		r.history = r.history[len(r.history)-12:]
	}
	return nil
}

func deterministicOperatorAnswer(cfg *config.Config, input string) (string, bool) {
	text := strings.ToLower(strings.TrimSpace(input))
	switch {
	case containsAny(text, "who created you", "who made you", "are you codex", "are you openai", "are you claude", "are you opencode"):
		return "I am 7review, the code-review agent in this repository. I run on the configured model provider; I should not claim to be Codex, OpenAI, Claude, or OpenCode.", true
	case containsAny(text, "what kind of model", "what model are you", "which model", "your model", "models are you"):
		return strings.Join([]string{
			"I am 7review's operator chat surface, backed by the configured model routing.",
			"Provider: " + cfg.Provider,
			"Review model: " + cfg.ReviewModel,
			"Formatter/chat model: " + cfg.SmallModel,
			"Embedding model: " + firstNonEmptyCommand(cfg.EmbeddingModel, "not configured"),
			"Orchestrator config: " + firstNonEmptyCommand(cfg.OrchestratorConfigPath, "env single-provider mode"),
			"Use `/providers` in run chat or `7review status --server <agent-url>` for live runtime status.",
		}, "\n"), true
	case containsAny(text, "context window", "context size", "context length", "token window"):
		return strings.Join([]string{
			"7review does not treat a diff hunk as the model context window.",
			"It builds review context from SCM diff, selected corpus, recalled memory, and Headroom compression.",
			"The exact context window depends on the configured provider/model; current routing is:",
			"Provider: " + cfg.Provider,
			"Review model: " + cfg.ReviewModel,
			"Formatter/chat model: " + cfg.SmallModel,
			"Embedding model: " + firstNonEmptyCommand(cfg.EmbeddingModel, "not configured"),
		}, "\n"), true
	default:
		return "", false
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func (r *modelChatResponder) operatorMemoryBlock(ctx context.Context, input string) string {
	memory := r.memory
	if memory == nil && r.cfg != nil && strings.TrimSpace(r.cfg.MemPalaceURL) != "" {
		store := tools.NewMemPalaceStore(r.cfg.MemPalaceURL, time.Duration(r.cfg.MemPalaceTimeout)*time.Millisecond)
		if r.cfg.EmbeddingModel != "" && r.cfg.OllamaBaseURL != "" {
			store.EmbeddingModel = r.cfg.EmbeddingModel
			store.Embedder = providers.NewOllama(r.cfg.OllamaBaseURL)
			store.EmbedQueries = true
		}
		memory = store
	}
	if memory == nil {
		return "retrieval: unavailable\nreason: MemPalace is not configured for operator chat"
	}
	recall, err := memory.Recall(ctx, review.Request{
		Provider:    "cli",
		ProjectID:   "local",
		Repository:  "operator-chat",
		ChangeID:    "chat",
		Title:       input,
		Description: strings.Join(r.history, "\n"),
	})
	if err != nil {
		return "retrieval: unavailable\nreason: " + err.Error()
	}
	return renderOperatorMemoryRecall(recall)
}

func renderOperatorMemoryRecall(recall review.MemoryRecall) string {
	lines := []string{"retrieval: mempalace"}
	lines = appendMemorySection(lines, "conventions", recall.Conventions)
	lines = appendMemorySection(lines, "decisions", recall.Decisions)
	lines = appendMemorySection(lines, "history", recall.History)
	if len(lines) == 1 {
		lines = append(lines, "no matching memory")
	}
	return strings.Join(lines, "\n")
}

func appendMemorySection(lines []string, label string, values []string) []string {
	if len(values) == 0 {
		return lines
	}
	lines = append(lines, label+":")
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lines = append(lines, "- "+truncateForPrompt(value, 500))
	}
	return lines
}

func truncateForPrompt(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func (r *modelChatResponder) userMessage(input string, memoryBlock string) string {
	var b strings.Builder
	if len(r.history) > 0 {
		b.WriteString("Recent chat history:\n")
		for _, item := range r.history {
			b.WriteString(item)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Retrieved memory and embedding-backed context for the small model:\n")
	b.WriteString(memoryBlock)
	b.WriteString("\n\n")
	b.WriteString("Structured task input:\n")
	b.WriteString("User message:\n")
	b.WriteString(input)
	return b.String()
}

func chatSystemPrompt(cfg *config.Config) string {
	instructions := readInstructions(cfg.InstructionsPath)
	toolNames := make([]string, 0, len(tools.Catalog()))
	for _, tool := range tools.Catalog() {
		toolNames = append(toolNames, "- "+tool.Name+": "+tool.Description)
	}
	return strings.Join([]string{
		"You are 7review's live engineering copilot, embedded in a production code-review agent.",
		"Identity rules:",
		"- Your product identity is 7review.",
		"- Do not claim to be Codex, Claude, OpenCode, OpenAI, Anthropic, Qwen, Ollama, or any other harness/provider.",
		"- If asked who created you, say you are the 7review code-review agent running on the configured model provider; do not invent a creator.",
		"- If asked what model you are, explain the configured provider and role routing shown below. Say the exact underlying model can only be verified from runtime configuration/logs.",
		"- If asked about context window size, do not describe diff hunks as a context window. Say 7review builds review context from SCM diff, selected corpus, memory, and Headroom compression; exact token limits depend on the configured model and role.",
		"- For small/formatter model answers, use the retrieved memory block in the user message as the only memory-backed knowledge. If it says retrieval is unavailable or no matching memory, say so plainly.",
		"Always-on instructions:",
		instructions,
		"Your job is to help engineers operate and iterate with the agent while preserving the review lifecycle: webhook -> SCM enrichment -> diff/context -> model review -> validation -> draft -> HIL approval -> final publish -> memory write.",
		"Communication rules:",
		"- Be concise, direct, and operational.",
		"- Always separate known state from assumptions.",
		"- Never invent runtime state, review results, dependency health, or SCM data.",
		"- If state is unknown, give the exact command or endpoint that verifies it.",
		"- Prefer one clear next command over broad advice.",
		"- When the engineer asks what to do next, answer with the next command and why it matters.",
		"- If the engineer asks about a finding, explain risk, evidence, and what would prove or disprove it.",
		"- Do not claim final approval, publish final reports, or write memory unless HIL approval is explicitly present.",
		"- Treat Headroom and MemPalace as required dependencies, not optional helpers.",
		"- Operator commands and curl examples need REVIEW_API_TOKEN in the environment or an Authorization bearer token.",
		"- Keep standard command names literal: setup, status, chat, docker compose, /ready, /runs, /run, /chat/stream.",
		"Available local commands:",
		"- 7review setup",
		"- 7review status",
		"- 7review status --server <agent-url>",
		"- 7review runs --server <agent-url>",
		"- 7review run <run-id> --server <agent-url>",
		"- 7review chat",
		"- 7review chat --run <run-id> --server <agent-url>",
		"- 7review approve --run <run-id> --report-file <path> --server <agent-url>",
		"- 7review approve --project <project-id> --mr <iid> --report-file <path> --server <agent-url>",
		"- 7review publish-final --run <run-id> --report-file <path> --server <agent-url>",
		"- docker compose up --build",
		"- curl <agent-url>/ready",
		"Model-facing tools:",
		strings.Join(toolNames, "\n"),
		"Configured endpoints:",
		"Headroom: " + cfg.HeadroomURL,
		"MemPalace: " + cfg.MemPalaceURL,
		"GitLab URL: " + cfg.GitLabURL,
		"GitHub API URL: " + cfg.GitHubAPIURL,
		"Configured model routing:",
		"Provider: " + cfg.Provider,
		"Review model: " + cfg.ReviewModel,
		"Small/formatter model: " + cfg.SmallModel,
		"Embedding model: " + firstNonEmptyCommand(cfg.EmbeddingModel, "not configured"),
		"Orchestrator config: " + cfg.OrchestratorConfigPath,
	}, "\n")
}

func readInstructions(path string) string {
	if path == "" {
		path = "./agent/instructions.md"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "agent instructions unavailable: " + err.Error()
	}
	return string(data)
}

type statusCommandOptions struct {
	serverURL string
	remote    bool
	plain     bool
}

type remoteReadiness struct {
	Ready        bool              `json:"ready"`
	Dependencies map[string]string `json:"dependencies"`
	Queue        remoteQueueStatus `json:"queue,omitempty"`
}

type remoteQueueStatus struct {
	Depth     int    `json:"depth"`
	Capacity  int    `json:"capacity"`
	Available int    `json:"available"`
	Enqueued  uint64 `json:"enqueued"`
	Completed uint64 `json:"completed"`
	Failed    uint64 `json:"failed"`
}

func runStatus(args []string, out io.Writer) error {
	opts := parseStatusArgs(args)
	if opts.remote {
		view, ready, err := remoteStatusView(operatorRequestHTTPClient(), opts)
		fmt.Fprintln(out, ui.RenderStatus(view))
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("agent is not ready")
		}
		return nil
	}
	view, ready := localStatusView(opts.plain)
	fmt.Fprintln(out, ui.RenderStatus(view))
	if !ready {
		return errors.New("local configuration is not ready")
	}
	return nil
}

func parseStatusArgs(args []string) statusCommandOptions {
	opts := statusCommandOptions{serverURL: "http://localhost:8080"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch flagName(arg) {
		case "--server":
			opts.serverURL = flagValue(args, &i)
			opts.remote = true
		case "--plain":
			opts.plain = true
		}
	}
	return opts
}

func localStatusView(plain bool) (ui.StatusView, bool) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return ui.StatusView{
			Title: "7review status",
			Plain: plain,
			Dependencies: []ui.DependencyStatus{
				{Name: "config", Ready: false, Detail: err.Error()},
			},
		}, false
	}
	return ui.StatusView{
		Title: "7review status",
		Plain: plain,
		Dependencies: []ui.DependencyStatus{
			{Name: "headroom", URL: cfg.HeadroomURL, Ready: cfg.HeadroomURL != ""},
			{Name: "mempalace", URL: cfg.MemPalaceURL, Ready: cfg.MemPalaceURL != ""},
		},
	}, true
}

func remoteStatusView(client *http.Client, opts statusCommandOptions) (ui.StatusView, bool, error) {
	serverURL := strings.TrimRight(opts.serverURL, "/")
	endpoint := serverURL + "/ready"
	statusCode, body, err := requestAgentRaw(client, http.MethodGet, endpoint, nil)
	if err != nil && body == "" {
		return ui.StatusView{
			Title: "7review status " + serverURL,
			Plain: opts.plain,
			Dependencies: []ui.DependencyStatus{{
				Name:   "agent",
				URL:    endpoint,
				Ready:  false,
				Detail: err.Error(),
			}},
		}, false, err
	}
	var ready remoteReadiness
	if decodeErr := json.Unmarshal([]byte(body), &ready); decodeErr != nil {
		return ui.StatusView{
			Title: "7review status " + serverURL,
			Plain: opts.plain,
			Dependencies: []ui.DependencyStatus{{
				Name:   "agent",
				URL:    endpoint,
				Ready:  false,
				Detail: "invalid readiness response: " + decodeErr.Error(),
			}},
		}, false, decodeErr
	}
	view := ui.StatusView{
		Title: "7review status " + serverURL,
		Plain: opts.plain,
		Dependencies: []ui.DependencyStatus{{
			Name:   "agent",
			URL:    endpoint,
			Ready:  ready.Ready && err == nil,
			Detail: fmt.Sprintf("http=%d", statusCode),
		}},
	}
	for name, detail := range ready.Dependencies {
		view.Dependencies = append(view.Dependencies, ui.DependencyStatus{
			Name:   name,
			Ready:  dependencyReady(detail),
			Detail: remoteDependencyDetail(name, detail, ready.Queue),
		})
	}
	return view, ready.Ready && err == nil, err
}

func dependencyReady(detail string) bool {
	detail = strings.TrimSpace(strings.ToLower(detail))
	return detail == "ok" || strings.HasPrefix(detail, "ok ")
}

func remoteDependencyDetail(name, detail string, queue remoteQueueStatus) string {
	if name != "queue" || queue.Capacity == 0 {
		return detail
	}
	return fmt.Sprintf("%s available=%d enqueued=%d completed=%d failed=%d", detail, queue.Available, queue.Enqueued, queue.Completed, queue.Failed)
}
