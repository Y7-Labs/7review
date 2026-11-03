package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/cmd/7review/operator"
)

type canonicalCommandOptions struct {
	attemptID, serverURL string
	json                 bool
	command              review.CancelAttemptCommand
}

type publicationCommandOptions struct {
	serverURL string
	json      bool
	command   review.AuthorizePublicationCommand
}

type findingDispositionOptions struct {
	attemptID, findingID, commandID, reason, serverURL string
	decision                                           review.FindingDecision
	json                                               bool
}

type usageSettlementOptions struct {
	serverURL string
	json      bool
	command   review.UsageSettlementCommand
}

const findingDispositionUsage = "7review disposition-finding <exact-attempt-id> <exact-finding-id> --command-id <id> --decision <accepted|rejected|disputed> --reason <text> [--server <url>] [--json]"

const publicationCommandUsage = "7review authorize-publication <exact-attempt-id> --command-id <id> --expected-version <n> --revision <revision> --assessment-digest <sha256> --artifact-digest <sha256> --provider <provider> --host <host> --repository-id <id> --change-number <number> --artifact-class <class> --reason <text> [--server <url>] [--json]"

const usageSettlementUsage = "7review settle-usage <exact-action-id> --command-id <id> --input-tokens <n> --output-tokens <n> --money-micro <n> --active-ms <n> --evidence-id <id> --reason <text> [--server <url>] [--json]"

func canonicalUsage(cancel bool) string {
	if cancel {
		return "7review cancel-attempt <exact-attempt-id> --command-id <id> --expected-version <n> --revision <revision> --reason <text> [--server <url>] [--json]"
	}
	return "7review attempt <exact-attempt-id> [--server <url>] [--json]"
}

func parseCanonicalOptions(args []string, cancel bool) (canonicalCommandOptions, error) {
	o := canonicalCommandOptions{serverURL: "http://localhost:8080"}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return o, fmt.Errorf("usage: %s", canonicalUsage(cancel))
	}
	o.attemptID = args[0]
	if err := review.ValidateOperatorAttemptID(o.attemptID); err != nil {
		return o, err
	}
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seen := map[string]bool{}
	var valueErr error
	set := func(name, value string, target *string) error {
		if seen[name] {
			valueErr = fmt.Errorf("duplicate --%s", name)
			return valueErr
		}
		seen[name] = true
		*target = value
		return nil
	}
	fs.Func("server", "operator server URL", func(v string) error { return set("server", v, &o.serverURL) })
	fs.BoolFunc("json", "emit JSON", func(v string) error {
		if seen["json"] {
			valueErr = errors.New("duplicate --json")
			return valueErr
		}
		seen["json"] = true
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			valueErr = errors.New("invalid --json value")
			return valueErr
		}
		o.json = parsed
		return nil
	})
	if cancel {
		o.command = review.CancelAttemptCommand{SchemaVersion: 1, AttemptID: o.attemptID, Kind: "cancel_attempt"}
		fs.Func("command-id", "stable command identity", func(v string) error { return set("command-id", v, &o.command.CommandID) })
		fs.Func("revision", "exact analysis revision", func(v string) error { return set("revision", v, &o.command.Revision) })
		fs.Func("reason", "cancellation audit reason", func(v string) error { return set("reason", v, &o.command.Payload.Reason) })
		fs.Func("expected-version", "observed control version", func(v string) error {
			var text string
			if err := set("expected-version", v, &text); err != nil {
				return err
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != v {
				valueErr = errors.New("--expected-version requires a positive canonical integer")
				return valueErr
			}
			o.command.ExpectedVersion = n
			return nil
		})
	}
	for n := 1; n < len(args); n++ {
		name, valueGiven := strings.CutPrefix(args[n], "--")
		if !valueGiven {
			name, valueGiven = strings.CutPrefix(args[n], "-")
		}
		if !valueGiven {
			return o, errors.New("unexpected positional operator arguments")
		}
		name, _, assigned := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil {
			return o, errors.New("unknown operator flag")
		}
		if assigned {
			continue
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if n+1 >= len(args) || strings.HasPrefix(args[n+1], "-") {
			return o, fmt.Errorf("--%s requires a value; use assignment syntax for leading-hyphen text", name)
		}
		n++
	}
	if err := fs.Parse(args[1:]); err != nil {
		if valueErr != nil {
			return o, valueErr
		}
		return o, fmt.Errorf("invalid operator flags; usage: %s", canonicalUsage(cancel))
	}
	if len(fs.Args()) != 0 {
		return o, errors.New("unexpected positional operator arguments")
	}
	if cancel {
		if err := o.command.Validate(); err != nil {
			return o, fmt.Errorf("all cancellation identity, version, revision and reason fields are required: %w", err)
		}
	}
	return o, nil
}

func parsePublicationOptions(args []string) (publicationCommandOptions, error) {
	o := publicationCommandOptions{serverURL: "http://localhost:8080"}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return o, fmt.Errorf("usage: %s", publicationCommandUsage)
	}
	if err := review.ValidateOperatorAttemptID(args[0]); err != nil {
		return o, err
	}
	o.command = review.AuthorizePublicationCommand{SchemaVersion: 1, AttemptID: args[0], Kind: "authorize_publication"}
	fs := flag.NewFlagSet("authorize-publication", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seen := map[string]bool{}
	var valueErr error
	set := func(name, value string, target *string) error {
		if seen[name] {
			valueErr = fmt.Errorf("duplicate --%s", name)
			return valueErr
		}
		seen[name] = true
		*target = value
		return nil
	}
	fs.Func("server", "operator server URL", func(v string) error { return set("server", v, &o.serverURL) })
	fs.BoolFunc("json", "emit JSON", func(v string) error {
		if seen["json"] {
			valueErr = errors.New("duplicate --json")
			return valueErr
		}
		seen["json"] = true
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			valueErr = errors.New("invalid --json value")
			return valueErr
		}
		o.json = parsed
		return nil
	})
	fs.Func("command-id", "stable command identity", func(v string) error { return set("command-id", v, &o.command.CommandID) })
	fs.Func("revision", "exact analysis revision", func(v string) error { return set("revision", v, &o.command.Revision) })
	fs.Func("assessment-digest", "validated assessment digest", func(v string) error { return set("assessment-digest", v, &o.command.Payload.AssessmentDigest) })
	fs.Func("artifact-digest", "exact draft artifact digest", func(v string) error { return set("artifact-digest", v, &o.command.Payload.ArtifactDigest) })
	fs.Func("provider", "destination provider", func(v string) error { return set("provider", v, &o.command.Payload.Destination.Provider) })
	fs.Func("host", "destination host", func(v string) error { return set("host", v, &o.command.Payload.Destination.Host) })
	fs.Func("repository-id", "destination repository identity", func(v string) error { return set("repository-id", v, &o.command.Payload.Destination.RepositoryID) })
	fs.Func("change-number", "destination change identity", func(v string) error { return set("change-number", v, &o.command.Payload.Destination.ChangeNumber) })
	fs.Func("artifact-class", "destination artifact class", func(v string) error { return set("artifact-class", v, &o.command.Payload.Destination.ArtifactClass) })
	fs.Func("reason", "authorization audit reason", func(v string) error { return set("reason", v, &o.command.Payload.Reason) })
	fs.Func("expected-version", "observed control version", func(v string) error {
		var text string
		if err := set("expected-version", v, &text); err != nil {
			return err
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != v {
			valueErr = errors.New("--expected-version requires a positive canonical integer")
			return valueErr
		}
		o.command.ExpectedVersion = n
		return nil
	})
	for n := 1; n < len(args); n++ {
		name, valueGiven := strings.CutPrefix(args[n], "--")
		if !valueGiven {
			name, valueGiven = strings.CutPrefix(args[n], "-")
		}
		if !valueGiven {
			return o, errors.New("unexpected positional operator arguments")
		}
		name, _, assigned := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil {
			return o, errors.New("unknown operator flag")
		}
		if assigned {
			continue
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if n+1 >= len(args) || strings.HasPrefix(args[n+1], "-") {
			return o, fmt.Errorf("--%s requires a value; use assignment syntax for leading-hyphen or empty text", name)
		}
		n++
	}
	if err := fs.Parse(args[1:]); err != nil {
		if valueErr != nil {
			return o, valueErr
		}
		return o, fmt.Errorf("invalid operator flags; usage: %s", publicationCommandUsage)
	}
	if len(fs.Args()) != 0 {
		return o, errors.New("unexpected positional operator arguments")
	}
	for _, required := range []string{"command-id", "expected-version", "revision", "assessment-digest", "artifact-digest", "provider", "host", "repository-id", "change-number", "artifact-class", "reason"} {
		if !seen[required] {
			return o, fmt.Errorf("--%s is required", required)
		}
	}
	if err := o.command.Validate(); err != nil {
		return o, fmt.Errorf("all publication identity, version, revision, artifact and destination fields are required: %w", err)
	}
	return o, nil
}

func parseFindingDispositionOptions(args []string) (findingDispositionOptions, error) {
	o := findingDispositionOptions{serverURL: "http://localhost:8080"}
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return o, fmt.Errorf("usage: %s", findingDispositionUsage)
	}
	o.attemptID, o.findingID = args[0], args[1]
	if err := review.ValidateOperatorAttemptID(o.attemptID); err != nil {
		return o, err
	}
	if err := review.ValidateOperatorFindingID(o.findingID); err != nil {
		return o, err
	}
	fs := flag.NewFlagSet("disposition-finding", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seen := map[string]bool{}
	set := func(name, value string, target *string) error {
		if seen[name] {
			return fmt.Errorf("duplicate --%s", name)
		}
		seen[name], *target = true, value
		return nil
	}
	fs.Func("command-id", "stable command identity", func(v string) error { return set("command-id", v, &o.commandID) })
	fs.Func("decision", "finding decision", func(v string) error {
		var value string
		if err := set("decision", v, &value); err != nil {
			return err
		}
		o.decision = review.FindingDecision(value)
		return nil
	})
	fs.Func("reason", "decision audit reason", func(v string) error { return set("reason", v, &o.reason) })
	fs.Func("server", "operator server URL", func(v string) error { return set("server", v, &o.serverURL) })
	fs.BoolFunc("json", "emit JSON", func(v string) error {
		if seen["json"] {
			return errors.New("duplicate --json")
		}
		seen["json"] = true
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid --json value")
		}
		o.json = parsed
		return nil
	})
	if err := fs.Parse(args[2:]); err != nil || len(fs.Args()) != 0 {
		return o, fmt.Errorf("invalid finding disposition flags; usage: %s", findingDispositionUsage)
	}
	for _, required := range []string{"command-id", "decision", "reason"} {
		if !seen[required] {
			return o, fmt.Errorf("--%s is required", required)
		}
	}
	probe := review.FindingDispositionCommand{SchemaVersion: 1, CommandID: o.commandID, AttemptID: o.attemptID, ExpectedVersion: 1, Revision: "probe", Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: o.findingID, FindingDigest: "sha256:" + strings.Repeat("0", 64), Decision: o.decision, Reason: o.reason}}
	if err := probe.Validate(); err != nil {
		return o, err
	}
	return o, nil
}

func parseUsageSettlementOptions(args []string) (usageSettlementOptions, error) {
	o := usageSettlementOptions{serverURL: "http://localhost:8080"}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return o, fmt.Errorf("usage: %s", usageSettlementUsage)
	}
	o.command = review.UsageSettlementCommand{SchemaVersion: 1, ActionID: args[0], Kind: "settle_model_usage"}
	fs := flag.NewFlagSet("settle-usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seen := map[string]bool{}
	set := func(name, value string, target *string) error {
		if seen[name] {
			return fmt.Errorf("duplicate --%s", name)
		}
		seen[name], *target = true, value
		return nil
	}
	setAmount := func(name, value string, target *int64) error {
		if seen[name] {
			return fmt.Errorf("duplicate --%s", name)
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != value {
			return fmt.Errorf("--%s requires a canonical nonnegative integer", name)
		}
		seen[name], *target = true, parsed
		return nil
	}
	fs.Func("command-id", "stable command identity", func(v string) error { return set("command-id", v, &o.command.CommandID) })
	fs.Func("input-tokens", "final input token usage", func(v string) error { return setAmount("input-tokens", v, &o.command.Payload.InputTokens) })
	fs.Func("output-tokens", "final output token usage", func(v string) error { return setAmount("output-tokens", v, &o.command.Payload.OutputTokens) })
	fs.Func("money-micro", "final cost in currency micro-units", func(v string) error { return setAmount("money-micro", v, &o.command.Payload.MoneyMicro) })
	fs.Func("active-ms", "final active provider time", func(v string) error { return setAmount("active-ms", v, &o.command.Payload.ActiveMS) })
	fs.Func("evidence-id", "external settlement evidence identity", func(v string) error { return set("evidence-id", v, &o.command.Payload.EvidenceID) })
	fs.Func("reason", "settlement audit reason", func(v string) error { return set("reason", v, &o.command.Payload.Reason) })
	fs.Func("server", "operator server URL", func(v string) error { return set("server", v, &o.serverURL) })
	fs.BoolFunc("json", "emit JSON", func(v string) error {
		if seen["json"] {
			return errors.New("duplicate --json")
		}
		seen["json"] = true
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid --json value")
		}
		o.json = parsed
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) != 0 {
		return o, fmt.Errorf("invalid usage settlement flags; usage: %s", usageSettlementUsage)
	}
	for _, required := range []string{"command-id", "input-tokens", "output-tokens", "money-micro", "active-ms", "evidence-id", "reason"} {
		if !seen[required] {
			return o, fmt.Errorf("--%s is required", required)
		}
	}
	if err := o.command.Validate(); err != nil {
		return o, err
	}
	return o, nil
}

func runOperatorAttempt(args []string, out io.Writer, httpClient *http.Client) error {
	return runOperatorAttemptContext(context.Background(), args, out, httpClient)
}
func runOperatorAttemptContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parseCanonicalOptions(args, false)
	if err != nil {
		return err
	}
	v, err := operator.NewClient(o.serverURL, httpClient).Attempt(ctx, o.attemptID)
	if err != nil {
		return fmt.Errorf("attempt %s: %w", o.attemptID, err)
	}
	if o.json {
		return json.NewEncoder(out).Encode(v)
	}
	_, err = fmt.Fprintf(out, "attempt %s\nrun %s\nrevision %q\nstate %s  phase %s  current %t  active %t\ncontrol version %d\nreleased unused %d  retained model reservations %d\ndeadline %s\n", v.AttemptID, v.RunID, v.Revision, v.InboxState, v.JobPhase, v.Current, v.Active, v.ControlVersion, v.ReleasedUnusedActions, v.RetainedModelReservations, v.Deadline.Format("2006-01-02T15:04:05Z07:00"))
	return err
}

func runOperatorActions(args []string, out io.Writer, httpClient *http.Client) error {
	return runOperatorActionsContext(context.Background(), args, out, httpClient)
}

func runOperatorActionsContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parseCanonicalOptions(args, false)
	if err != nil {
		return err
	}
	view, err := operator.NewClient(o.serverURL, httpClient).Actions(ctx, o.attemptID)
	if err != nil {
		return fmt.Errorf("attempt %s actions: %w", o.attemptID, err)
	}
	if o.json {
		err = json.NewEncoder(out).Encode(view)
	} else {
		_, err = fmt.Fprintf(out, "actions for attempt %s  %d\n", view.AttemptID, len(view.Actions))
		for _, action := range view.Actions {
			if err != nil {
				break
			}
			settlement := "unsettled"
			if action.Settled {
				settlement = "settled"
			}
			if action.IntegrityIncident {
				settlement = "integrity-incident"
			}
			_, err = fmt.Fprintf(out, "%s  %s  %s/%s  %s\n  upper input=%d output=%d money-micro=%d active-ms=%d\n", action.ActionID, action.State, action.Provider, action.Model, settlement, action.Upper.InputTokens, action.Upper.OutputTokens, action.Upper.MoneyMicro, action.Upper.ActiveMS)
			if err == nil && action.Actual != nil {
				_, err = fmt.Fprintf(out, "  actual input=%d output=%d money-micro=%d active-ms=%d  receipt=%s  evidence=%s\n", action.Actual.InputTokens, action.Actual.OutputTokens, action.Actual.MoneyMicro, action.Actual.ActiveMS, action.ReceiptID, action.EvidenceRef)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("action projection received but output failed: %w", err)
	}
	return nil
}

func runCancelAttempt(args []string, out io.Writer, httpClient *http.Client) error {
	return runCancelAttemptContext(context.Background(), args, out, httpClient)
}
func runCancelAttemptContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parseCanonicalOptions(args, true)
	if err != nil {
		return err
	}
	r, err := operator.NewClient(o.serverURL, httpClient).CancelAttempt(ctx, o.command)
	if err != nil {
		return fmt.Errorf("command %s: %w", o.command.CommandID, err)
	}
	if o.json {
		err = json.NewEncoder(out).Encode(r)
	} else {
		_, err = fmt.Fprintf(out, "cancelled %s\ncommand %s  actor %s  control version %d\nreservation disposition %s (cancellation receipt)\n", r.AttemptID, r.CommandID, r.ActorID, r.ControlVersion, r.ReservationDisposition)
	}
	if err != nil {
		return fmt.Errorf("cancellation acknowledged for command %s but output failed: %w", r.CommandID, err)
	}
	return nil
}

func runAuthorizePublication(args []string, out io.Writer, httpClient *http.Client) error {
	return runAuthorizePublicationContext(context.Background(), args, out, httpClient)
}

func runAuthorizePublicationContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parsePublicationOptions(args)
	if err != nil {
		return err
	}
	r, err := operator.NewClient(o.serverURL, httpClient).AuthorizePublication(ctx, o.command)
	if err != nil {
		return fmt.Errorf("command %s: %w", o.command.CommandID, err)
	}
	if o.json {
		err = json.NewEncoder(out).Encode(r)
	} else {
		destination := r.Destination.Provider + "://" + r.Destination.Host + "/" + r.Destination.RepositoryID
		if r.Destination.Host == "" {
			destination = r.Destination.Provider + ":" + r.Destination.RepositoryID
		}
		_, err = fmt.Fprintf(out, "authorized publication for %s\ncommand %s  actor %s  control version %d\ndestination %s change %s  class %s\n", r.AttemptID, r.CommandID, r.ActorID, r.ControlVersion, destination, r.Destination.ChangeNumber, r.Destination.ArtifactClass)
	}
	if err != nil {
		return fmt.Errorf("publication authorization acknowledged for command %s but output failed: %w", r.CommandID, err)
	}
	return nil
}

func runDispositionFinding(args []string, out io.Writer, httpClient *http.Client) error {
	return runDispositionFindingContext(context.Background(), args, out, httpClient)
}

func runDispositionFindingContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parseFindingDispositionOptions(args)
	if err != nil {
		return err
	}
	client := operator.NewClient(o.serverURL, httpClient)
	view, err := client.FindingDisposition(ctx, o.attemptID, o.findingID)
	if err != nil {
		return fmt.Errorf("finding %s: %w", o.findingID, err)
	}
	expectedVersion := view.ControlVersion
	if view.Current != nil && view.Current.CommandID == o.commandID {
		if view.Current.Decision != o.decision || view.Current.Reason != o.reason || view.Current.RecordedVersion < 2 {
			return fmt.Errorf("command %s already identifies a different finding disposition", o.commandID)
		}
		expectedVersion = view.Current.RecordedVersion - 1
	}
	command := review.FindingDispositionCommand{SchemaVersion: 1, CommandID: o.commandID, AttemptID: o.attemptID, ExpectedVersion: expectedVersion, Revision: view.Revision, Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: o.findingID, FindingDigest: view.FindingDigest, Decision: o.decision, Reason: o.reason}}
	receipt, err := client.DispositionFinding(ctx, command)
	if err != nil {
		return fmt.Errorf("command %s: %w", command.CommandID, err)
	}
	if o.json {
		err = json.NewEncoder(out).Encode(receipt)
	} else {
		_, err = fmt.Fprintf(out, "%s finding %s for %s\ncommand %s  actor %s  control version %d\n", receipt.Decision, receipt.FindingID, receipt.AttemptID, receipt.CommandID, receipt.ActorID, receipt.ControlVersion)
	}
	if err != nil {
		return fmt.Errorf("finding disposition acknowledged for command %s but output failed: %w", receipt.CommandID, err)
	}
	return nil
}

func runSettleUsage(args []string, out io.Writer, httpClient *http.Client) error {
	return runSettleUsageContext(context.Background(), args, out, httpClient)
}

func runSettleUsageContext(ctx context.Context, args []string, out io.Writer, httpClient *http.Client) error {
	o, err := parseUsageSettlementOptions(args)
	if err != nil {
		return err
	}
	receipt, err := operator.NewClient(o.serverURL, httpClient).SettleUsage(ctx, o.command)
	if err != nil {
		return fmt.Errorf("command %s: %w", o.command.CommandID, err)
	}
	if o.json {
		err = json.NewEncoder(out).Encode(receipt)
	} else {
		_, err = fmt.Fprintf(out, "settled usage for action %s\ncommand %s  actor %s\ninput %d  output %d  money-micro %d  active-ms %d\nevidence %s\n", receipt.ActionID, receipt.CommandID, receipt.ActorID, receipt.Actual.InputTokens, receipt.Actual.OutputTokens, receipt.Actual.MoneyMicro, receipt.Actual.ActiveMS, receipt.EvidenceRef)
	}
	if err != nil {
		return fmt.Errorf("usage settlement acknowledged for command %s but output failed: %w", receipt.CommandID, err)
	}
	return nil
}
