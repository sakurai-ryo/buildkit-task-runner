// Package runner connects to buildkitd and solves (executes) the LLB.
package runner

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/debug"
)

// Address resolves the buildkitd address to connect to.
// Priority: explicit addr > BUILDKIT_HOST env var > default unix socket.
func Address(addr string) string {
	if addr != "" {
		return addr
	}
	if env := os.Getenv("BUILDKIT_HOST"); env != "" {
		return env
	}
	return "unix:///run/buildkit/buildkitd.sock"
}

// Platform returns the LLB platform from the --platform string, or the host GOARCH if empty.
func Platform(name string) llb.ConstraintsOpt {
	switch name {
	case "linux/amd64", "amd64":
		return llb.Platform(specs.Platform{OS: "linux", Architecture: "amd64"})
	case "linux/arm64", "arm64":
		return llb.Platform(specs.Platform{OS: "linux", Architecture: "arm64"})
	case "":
		if runtime.GOARCH == "arm64" {
			return llb.LinuxArm64
		}
		return llb.LinuxAmd64
	default:
		return llb.Platform(specs.Platform{OS: "linux", Architecture: name})
	}
}

// Run solves (executes) st on buildkitd and prints progress to stdout.
// localMounts maps llb.Local names to local directories to expose to the daemon.
func Run(ctx context.Context, addr string, platform llb.ConstraintsOpt, st llb.State, localMounts map[string]string) error {
	debug.Logf("runner: connecting to buildkitd at %s", addr)
	c, err := client.New(ctx, addr)
	if err != nil {
		return fmt.Errorf("failed to connect to buildkitd (%s): %w", addr, err)
	}
	defer c.Close()

	def, err := st.Marshal(ctx, platform)
	if err != nil {
		return fmt.Errorf("failed to marshal LLB: %w", err)
	}
	debug.Logf("runner: marshaled LLB definition (%d operations in the graph)", len(def.Def))

	solveOpt := client.SolveOpt{}
	if len(localMounts) > 0 {
		mounts := make(map[string]fsutil.FS, len(localMounts))
		for name, dir := range localMounts {
			fs, err := fsutil.NewFS(dir)
			if err != nil {
				return fmt.Errorf("failed to expose local directory %q: %w", dir, err)
			}
			mounts[name] = fs
			debug.Logf("runner: exposing local mount %q -> %s", name, dir)
		}
		solveOpt.LocalMounts = mounts
	}

	ch := make(chan *client.SolveStatus)
	done := make(chan struct{})
	go func() {
		printStatus(ch)
		close(done)
	}()

	debug.Logf("runner: solving (buildkitd runs the graph with parallelism and caching)...")
	_, err = c.Solve(ctx, def, solveOpt, ch)
	<-done
	if err != nil {
		return fmt.Errorf("solve failed: %w", err)
	}
	debug.Logf("runner: solve completed")
	return nil
}

// printStatus consumes the status channel and prints each step name and its logs plainly.
func printStatus(ch chan *client.SolveStatus) {
	for s := range ch {
		for _, v := range s.Vertexes {
			switch {
			case v.Started != nil && v.Completed == nil:
				fmt.Printf("▶ %s\n", v.Name)
			case v.Completed != nil:
				status := "done"
				if v.Error != "" {
					status = "ERROR: " + v.Error
				} else if v.Cached {
					status = "cached"
				}
				fmt.Printf("✔ %s (%s)\n", v.Name, status)
			}
		}
		for _, l := range s.Logs {
			for _, line := range strings.Split(strings.TrimRight(string(l.Data), "\n"), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}
