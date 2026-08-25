package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/controlplane"
	"github.com/owainlewis/factory-v2/internal/managedworker"
	"github.com/owainlewis/factory-v2/internal/runner"
	"github.com/spf13/cobra"
)

type commandOptions struct {
	configPath        string
	factoryConfigPath string
	agentName         string
	pipelineName      string
	prompt            string
	model             string
	repository        string
	stdin             io.Reader
	stdout            io.Writer
	stderr            io.Writer
	version           string
	listen            string
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
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "configuration file")
	root.AddCommand(newInitCommand(options))
	root.AddCommand(newRunCommand(options, true))
	root.AddCommand(newStartCommand(options))

	worker := &cobra.Command{Use: "worker", Short: "Run or connect a Factory Worker"}
	worker.AddCommand(newRunCommand(options, false))
	worker.AddCommand(newWorkerStartCommand(options))
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

func newStartCommand(options *commandOptions) *cobra.Command {
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the Factory control plane",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			factoryConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			serverConfig := factoryConfig.Server
			if options.listen != "" {
				serverConfig.Listen = options.listen
			}
			token, err := serverConfig.WorkerToken()
			if err != nil {
				return err
			}
			store, err := controlplane.OpenStore(serverConfig.Database)
			if err != nil {
				return err
			}
			defer store.Close()
			server, err := controlplane.NewServer(store, factoryConfig.Path(), token)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stderr, "factory: control plane listening on http://%s\n", serverConfig.Listen)
			return server.Serve(command.Context(), serverConfig.Listen)
		},
	}
	start.Flags().StringVar(&options.listen, "listen", "", "loopback listen address (default 127.0.0.1:7331)")
	return start
}

func newWorkerStartCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start a managed Factory Worker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			workerConfig, err := config.LoadWorker(options.configPath)
			if err != nil {
				return err
			}
			worker, err := managedworker.New(workerConfig, options.stdout, options.stderr)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stderr, "factory: worker %s connecting to %s\n", workerConfig.Name, workerConfig.ControlPlane.URL)
			return worker.Run(command.Context())
		},
	}
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
	run.Flags().StringVar(&options.model, "model", "", "executor model or configured alias for this task")
	run.Flags().StringVar(&options.repository, "repo", ".", "Git repository path")
	run.Flags().StringVar(&options.factoryConfigPath, "factory-config", "", "shared Factory configuration file")
	_ = run.MarkFlagRequired("prompt")
	return run
}

func runSelection(ctx context.Context, options *commandOptions) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	definitionPath, err := worker.ResolveFactoryConfig(options.factoryConfigPath)
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
	agent, err = worker.ResolveAgentModel(agent, options.model)
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
