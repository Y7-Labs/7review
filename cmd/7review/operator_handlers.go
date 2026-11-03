package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Y4NN777/7review/agent/ui"
	"github.com/Y4NN777/7review/cmd/7review/operator"
)

type operatorCommandContext struct {
	Context   context.Context
	ServerURL string
	RunID     string
	Client    *http.Client
	Fields    []string
	Out       io.Writer
	Options   ui.ChatOptions
}

type operatorCommandHandler func(operatorCommandContext) error

var operatorCommandHandlers = map[string]operatorCommandHandler{
	"/attempt":               handleOperatorAttemptCommand,
	"/actions":               handleOperatorActionsCommand,
	"/cancel-attempt":        handleCancelAttemptCommand,
	"/authorize-publication": handleAuthorizePublicationCommand,
	"/disposition-finding":   handleDispositionFindingCommand,
	"/settle-usage":          handleSettleUsageCommand,
	"/help":                  handleHelpCommand,
	"/status":                handleStatusCommand,
	"/tools":                 handleToolsCommand,
	"/providers":             handleProvidersCommand,
	"/config":                handleConfigCommand,
	"/skills":                handleSkillsCommand,
	"/sessions":              handleSessionsCommand,
	"/diff":                  handleDiffCommand,
	"/context":               handleContextCommand,
	"/history":               handleHistoryCommand,
	"/memory":                handleMemoryCommand,
	"/run":                   handleRunCommand,
	"/draft":                 handleDraftCommand,
	"/approve":               handleApproveCommand,
	"/publish-final":         handlePublishFinalCommand,
}

func chatCommandHandlerWithClient(serverURL, runID string, client *http.Client) ui.ChatCommandFunc {
	return func(ctx context.Context, text string, out io.Writer, _ ui.ChatContext, opts ui.ChatOptions) (bool, error) {
		fields, err := parseChatCommandFields(text)
		if err != nil {
			return true, err
		}
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
			return false, nil
		}
		command := operator.CanonicalCommand(fields[0])
		handler, ok := operatorCommandHandlers[command]
		if !ok {
			return true, fmt.Errorf("unknown chat command %q; use /help", fields[0])
		}
		err = handler(operatorCommandContext{
			Context:   ctx,
			ServerURL: serverURL,
			RunID:     runID,
			Client:    client,
			Fields:    fields,
			Out:       out,
			Options:   opts,
		})
		return true, err
	}
}

func handleOperatorAttemptCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runOperatorAttemptContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleOperatorActionsCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runOperatorActionsContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleCancelAttemptCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runCancelAttemptContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleAuthorizePublicationCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runAuthorizePublicationContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleDispositionFindingCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runDispositionFindingContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleSettleUsageCommand(ctx operatorCommandContext) error {
	var out strings.Builder
	args := append(append([]string(nil), ctx.Fields[1:]...), "--server", ctx.ServerURL)
	if err := runSettleUsageContext(ctx.Context, args, &out, ctx.Client); err != nil {
		return err
	}
	writeAgentMessage(ctx, strings.TrimSpace(out.String()))
	return nil
}

func handleHelpCommand(ctx operatorCommandContext) error {
	writeAgentMessage(ctx, chatCommandHelp(ctx.RunID != ""))
	return nil
}

func handleStatusCommand(ctx operatorCommandContext) error {
	statusView, _, _ := remoteStatusView(ctx.Client, statusCommandOptions{serverURL: ctx.ServerURL, remote: true, plain: true})
	writeAgentMessage(ctx, ui.RenderStatus(statusView))
	return nil
}

func handleToolsCommand(ctx operatorCommandContext) error {
	var catalog []ui.ToolRow
	if err := getJSON(ctx.Client, strings.TrimRight(ctx.ServerURL, "/")+"/tools", &catalog); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderToolCatalogSummary(catalog))
	return nil
}

func handleProvidersCommand(ctx operatorCommandContext) error {
	var status remoteProviderStatus
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "list_provider_status", nil, &status); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderProviderStatusSummary(status))
	return nil
}

func handleConfigCommand(ctx operatorCommandContext) error {
	var status remoteConfigStatus
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "get_config_status", nil, &status); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderConfigStatusSummary(status))
	return nil
}

func handleSkillsCommand(ctx operatorCommandContext) error {
	var skills []remoteSkillStatus
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "list_skills", nil, &skills); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderSkillStatusSummary(skills))
	return nil
}

func handleSessionsCommand(ctx operatorCommandContext) error {
	sessionOpts := parseSessionsArgs(append([]string{"--server", ctx.ServerURL}, ctx.Fields[1:]...))
	var runs []remoteRunRow
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "list_runs", nil, &runs); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderSessionsSummary(runs, sessionOpts))
	return nil
}

func handleDiffCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/diff"); err != nil {
		return err
	}
	var summary remoteDiffSummary
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "get_diff_summary", map[string]any{"run": ctx.RunID}, &summary); err != nil {
		return err
	}
	writeAgentMessage(ctx, operator.RenderDiffSummary(summary))
	return nil
}

func handleContextCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/context"); err != nil {
		return err
	}
	var selected remoteSelectedContext
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "get_selected_context", map[string]any{"run": ctx.RunID}, &selected); err != nil {
		return err
	}
	writeAgentMessage(ctx, operator.RenderSelectedContextSummary(selected))
	return nil
}

func handleHistoryCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/history"); err != nil {
		return err
	}
	historyOpts := historyCommandOptions{serverURL: ctx.ServerURL, runID: ctx.RunID}
	if len(ctx.Fields) > 1 {
		historyOpts.eventType = ctx.Fields[1]
	}
	if len(ctx.Fields) > 2 {
		historyOpts.limit = parsePositiveInt(ctx.Fields[2])
	}
	detail, err := fetchRemoteRunDetail(ctx.Client, ctx.ServerURL, ctx.RunID)
	if err != nil {
		return err
	}
	writeAgentMessage(ctx, renderRunHistory(detail, historyOpts))
	return nil
}

func handleMemoryCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/memory"); err != nil {
		return err
	}
	var status remoteMemoryProposalStatus
	if err := executeRemoteTool(ctx.Client, ctx.ServerURL, "preview_memory_proposal", map[string]any{"run": ctx.RunID}, &status); err != nil {
		return err
	}
	writeAgentMessage(ctx, renderMemoryProposalSummary(status))
	return nil
}

func handleRunCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/run"); err != nil {
		return err
	}
	detail, err := fetchRemoteRunDetail(ctx.Client, ctx.ServerURL, ctx.RunID)
	if err != nil {
		return err
	}
	writeAgentMessage(ctx, renderRunSnapshot(detail))
	return nil
}

func handleDraftCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/draft"); err != nil {
		return err
	}
	detail, err := fetchRemoteRunDetail(ctx.Client, ctx.ServerURL, ctx.RunID)
	if err != nil {
		return err
	}
	message, err := renderOrWriteDraft(detail, ctx.Fields[1:])
	if err != nil {
		return err
	}
	writeAgentMessage(ctx, message)
	return nil
}

func handleApproveCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/approve"); err != nil {
		return err
	}
	if !hasFlag(ctx.Fields[1:], "--report-file") {
		return fmt.Errorf("/approve requires --report-file <path>")
	}
	approval, err := parseApprovalArgs(append([]string{"--server", ctx.ServerURL, "--run", ctx.RunID}, ctx.Fields[1:]...))
	if err != nil {
		return err
	}
	if err := submitApproval(ctx.Client, approval); err != nil {
		return err
	}
	writeAgentMessage(ctx, "approval queued for "+approval.approvalTarget())
	return nil
}

func handlePublishFinalCommand(ctx operatorCommandContext) error {
	if err := requireRun(ctx, "/publish-final"); err != nil {
		return err
	}
	if !hasFlag(ctx.Fields[1:], "--report-file") {
		return fmt.Errorf("/publish-final requires --report-file <path>")
	}
	publish, err := parsePublishArgs(append([]string{"--server", ctx.ServerURL, "--run", ctx.RunID}, ctx.Fields[1:]...))
	if err != nil {
		return err
	}
	if err := submitFinalPublish(ctx.Client, publish); err != nil {
		return err
	}
	writeAgentMessage(ctx, "final publish queued for "+publish.runID)
	return nil
}

func requireRun(ctx operatorCommandContext, command string) error {
	if ctx.RunID != "" {
		return nil
	}
	return fmt.Errorf("%s requires chat <run-id> or --run <run-id>", command)
}

func writeAgentMessage(ctx operatorCommandContext, text string) {
	fmt.Fprintln(ctx.Out, ui.RenderChatMessage(ui.ChatMessage{Role: "agent", Text: text}, ctx.Options.Plain))
}
