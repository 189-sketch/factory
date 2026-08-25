package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/runner"
	"github.com/spf13/cobra"
)

type commandOptions struct {
	configPath     string
	definitionPath string
	agentName      string
	pipelineName   string
	prompt         string
	repository     string
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
	version        string
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	options := &commandOptions{stdin: stdin, stdout: stdout, stderr: stderr, version: version}
	root := newRootCommand(options)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var outcome *runner.OutcomeError
	if errors.As(err, &outcome) {
		fmt.Fprintf(stderr, "factory: %s\n", outcome.Error())
		return outcome.ExitCode
	}
	var runtime *runner.RuntimeError
	if errors.As(err, &runtime) {
		fmt.Fprintf(stderr, "factory: %s\n", runtime.Error())
		return 1
	}
	fmt.Fprintf(stderr, "factory: %s\n", err)
	return 2
}

func newRootCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{
		Use:           "factory",
		Short:         "Run coding agents as supervised workloads",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "worker configuration file (default ~/.factory/worker.toml)")
	root.AddCommand(newRunCommand(options, true))

	worker := &cobra.Command{Use: "worker", Short: "Run or connect a Factory Worker"}
	worker.AddCommand(newRunCommand(options, false))
	root.AddCommand(worker)

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the Factory version",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Fprintln(options.stdout, options.version)
		},
	})
	return root
}

func newRunCommand(options *commandOptions, allowPipeline bool) *cobra.Command {
	short := "Run one configured agent in a Git repository"
	if allowPipeline {
		short = "Run a configured agent or pipeline in a Git repository"
	}
	run := &cobra.Command{
		Use:   "run",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runSelection(command.Context(), options)
		},
	}
	run.Flags().StringVar(&options.agentName, "agent", "", "agent name from the Factory definition")
	if allowPipeline {
		run.Flags().StringVar(&options.pipelineName, "pipeline", "", "pipeline name from the Factory definition")
		run.MarkFlagsMutuallyExclusive("agent", "pipeline")
		run.MarkFlagsOneRequired("agent", "pipeline")
	} else {
		_ = run.MarkFlagRequired("agent")
	}
	run.Flags().StringVar(&options.prompt, "prompt", "", "work request supplied to the agent prompt (required)")
	run.Flags().StringVar(&options.repository, "repo", ".", "Git repository path")
	run.Flags().StringVar(&options.definitionPath, "definition", "", "Factory definition file")
	_ = run.MarkFlagRequired("prompt")
	return run
}

func runSelection(ctx context.Context, options *commandOptions) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	definitionPath, err := worker.ResolveDefinition(options.definitionPath)
	if err != nil {
		return err
	}
	if options.pipelineName == "" {
		agent, err := config.LoadAgent(definitionPath, options.agentName)
		if err != nil {
			return err
		}
		return runAgent(ctx, options, worker, agent)
	}
	agents, err := config.LoadPipeline(definitionPath, options.pipelineName)
	if err != nil {
		return err
	}
	for index, agent := range agents {
		fmt.Fprintf(options.stderr, "factory: pipeline %s: agent %d/%d %s\n", options.pipelineName, index+1, len(agents), agent.Name)
		if err := runAgent(ctx, options, worker, agent); err != nil {
			return err
		}
	}
	return nil
}

func runAgent(ctx context.Context, options *commandOptions, worker config.Worker, agent config.ResolvedAgent) error {
	var err error
	agent, err = config.RenderPrompt(agent, options.prompt)
	if err != nil {
		return err
	}
	result, err := runner.Execute(ctx, runner.Options{
		Agent:         agent,
		Repository:    options.repository,
		DataDirectory: worker.DataDirectory,
		Stdout:        options.stdout,
		Stderr:        options.stderr,
	})
	if result.ID != "" {
		fmt.Fprintf(options.stderr, "factory: run %s %s; events: %s\n", result.ID, result.State, result.EventsPath)
	}
	return err
}
