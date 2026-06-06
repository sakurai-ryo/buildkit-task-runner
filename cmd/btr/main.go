// Command btr is a lightweight task runner that uses BuildKit as its execution engine.
// Tasks are declared in YAML, compiled into LLB states, and executed on buildkitd.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/config"
	"github.com/sakurai-ryo/buildkit-task-runner/internal/debug"
	"github.com/sakurai-ryo/buildkit-task-runner/internal/graph"
	"github.com/sakurai-ryo/buildkit-task-runner/internal/llbgen"
	"github.com/sakurai-ryo/buildkit-task-runner/internal/runner"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var dbg bool
	root := &cobra.Command{
		Use:          "btr",
		Short:        "BuildKit Task Runner",
		Long:         "btr declares tasks in YAML, compiles each into a BuildKit LLB state, and executes them on buildkitd.",
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Enable debug logging from the --debug flag or BTR_DEBUG env var.
			debug.SetEnabled(dbg || os.Getenv("BTR_DEBUG") != "")
		},
	}
	root.PersistentFlags().BoolVar(&dbg, "debug", false, "print debug logs explaining each step (or set BTR_DEBUG)")
	root.AddCommand(newRunCmd(), newListCmd(), newGraphCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var (
		file     string
		addr     string
		platform string
	)
	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "Run a task and its dependencies on buildkitd",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			cfg, err := config.Load(file)
			if err != nil {
				return err
			}
			if err := graph.Resolve(cfg, target); err != nil {
				return err
			}

			builder := llbgen.New(cfg)
			st, err := builder.State(target)
			if err != nil {
				return err
			}

			resolvedAddr := runner.Address(addr)
			fmt.Printf("buildkitd=%s task=%s\n", resolvedAddr, target)
			return runner.Run(cmd.Context(), resolvedAddr, runner.Platform(platform), st, builder.LocalMounts())
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "tasks.yaml", "task definition file")
	cmd.Flags().StringVar(&addr, "addr", "", "buildkitd address (e.g. tcp://127.0.0.1:1234)")
	cmd.Flags().StringVar(&platform, "platform", "", "target platform (e.g. linux/arm64)")
	return cmd
}

func newGraphCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "graph [task]",
		Short: "Print the task dependency graph as a Mermaid flowchart",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
			if err != nil {
				return err
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			out, err := graph.Mermaid(cfg, target)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "tasks.yaml", "task definition file")
	return cmd
}

func newListCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List defined tasks and their dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
			if err != nil {
				return err
			}

			names := make([]string, 0, len(cfg.Tasks))
			for name := range cfg.Tasks {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				t := cfg.Tasks[name]
				if len(t.Deps) > 0 {
					fmt.Printf("%-16s (deps: %v)\n", name, t.Deps)
				} else {
					fmt.Printf("%s\n", name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "tasks.yaml", "task definition file")
	return cmd
}
