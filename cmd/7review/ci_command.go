package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	cirunner "github.com/Y4NN777/7review/agent/ci"
	"github.com/Y4NN777/7review/agent/orchestrator"
)

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runCICommand(args []string, out io.Writer) (int, error) {
	if len(args) == 0 || args[0] != "review" {
		return 2, fmt.Errorf("usage: 7review ci review --policy-path .7review/review.yaml --output .7review/out [--quality provider:id:path]")
	}
	flags := flag.NewFlagSet("ci review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy-path", ".7review/review.yaml", "policy path at the base revision")
	output := flags.String("output", ".7review/out", "artifact output directory")
	repositoryDir := flags.String("repo-dir", ".", "local Git repository")
	skillsPath := flags.String("skills-path", ".7review/skills", "skills path in the frozen analysis tree")
	deadline := flags.Duration("deadline", 10*time.Minute, "maximum review duration")
	offline := flags.Bool("offline", false, "forbid fetching missing Git objects")
	provider := flags.String("provider", "", "CI provider override")
	repository := flags.String("repository", "", "repository identity override")
	base := flags.String("base", "", "base revision override")
	head := flags.String("head", "", "source head revision override")
	merge := flags.String("merge", "", "synthetic merge revision override")
	pipelineID := flags.String("pipeline-id", "", "pipeline identity override")
	jobID := flags.String("job-id", "", "job identity override")
	changeID := flags.String("change-id", "", "pull or merge request identity override")
	var qualities repeatedFlag
	flags.Var(&qualities, "quality", "verified CI quality artifact provider:id:path")
	if err := flags.Parse(args[1:]); err != nil {
		return 2, err
	}
	if flags.NArg() != 0 || *deadline <= 0 {
		return 2, fmt.Errorf("CI review has unexpected arguments or a non-positive deadline")
	}
	environment, err := cirunner.DetectEnvironment(cirunner.EnvironmentOverrides{
		Provider: *provider, RepositoryID: *repository, BaseRevision: *base, HeadRevision: *head,
		MergeRevision: *merge, PipelineID: *pipelineID, JobID: *jobID, ChangeID: *changeID,
	})
	if err != nil {
		return 2, err
	}
	artifactRefs := make([]cirunner.ArtifactRef, 0, len(qualities))
	for _, value := range qualities {
		ref, err := cirunner.ParseArtifactRef(value)
		if err != nil {
			return 2, err
		}
		if ref.Provider != environment.Provider {
			return 2, fmt.Errorf("quality artifact provider %q does not match CI provider %q", ref.Provider, environment.Provider)
		}
		artifactRefs = append(artifactRefs, ref)
	}
	var orch *orchestrator.Orchestrator
	if !environment.UntrustedFork {
		orch, _, err = cirunner.BuildModelOrchestratorFromEnvironment()
		if err != nil {
			return 2, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()
	result, err := cirunner.Run(ctx, cirunner.Options{
		RepositoryDir: *repositoryDir, PolicyPath: *policyPath, SkillsPath: *skillsPath,
		Offline: *offline, Environment: environment, DeadlineAt: time.Now().UTC().Add(*deadline),
		Quality: artifactRefs, Orchestrator: orch,
	})
	if err != nil {
		return 2, err
	}
	if err := cirunner.Export(result, *output); err != nil {
		return 2, err
	}
	fmt.Fprintf(out, "7review CI: outcome=%s mode=%s exit=%d output=%s\n", result.Gate.Outcome, result.Gate.Mode, cirunner.ExitCode(result), *output)
	return cirunner.ExitCode(result), nil
}
