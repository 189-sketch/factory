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

	worker := &cobra.Command{Use: "worker", Short: "Run or connect a Factory Worker"}
	run := &cobra.Command{
		Use:   "run",
		Short: "Run one configured agent in a Git repository",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runAgent(command.Context(), options)
		},
	}
	run.Flags().StringVar(&options.agentName, "agent", "", "agent name from the Factory definition (required)")
	run.Flags().StringVar(&options.repository, "repo", ".", "Git repository path")
	run.Flags().StringVar(&options.definitionPath, "definition", "", "Factory definition file")
	_ = run.MarkFlagRequired("agent")
	worker.AddCommand(run)
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

func runAgent(ctx context.Context, options *commandOptions) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	definitionPath, err := worker.ResolveDefinition(options.definitionPath)
	if err != nil {
		return err
	}
	agent, err := config.LoadAgent(definitionPath, options.agentName)
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
