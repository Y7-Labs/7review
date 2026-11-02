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

const publicationCommandUsage = "7review authorize-publication <exact-attempt-id> --command-id <id> --expected-version <n> --revision <revision> --assessment-digest <sha256> --artifact-digest <sha256> --provider <provider> --host <host> --repository-id <id> --change-number <number> --artifact-class <class> --reason <text> [--server <url>] [--json]"

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
