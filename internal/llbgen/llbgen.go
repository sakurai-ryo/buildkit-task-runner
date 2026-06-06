// Package llbgen converts task definitions into a BuildKit LLB state graph.
package llbgen

import (
	"fmt"
	"path/filepath"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/config"
)

// defaultWorkdir is used when a task has a source but no explicit dir.
const defaultWorkdir = "/src"

// Builder memoizes task name -> llb.State conversions so shared deps are built only once.
// It also collects the local directories that must be exposed to buildkitd as local mounts.
type Builder struct {
	cfg    *config.Config
	memo   map[string]llb.State
	mounts map[string]string // local mount name -> absolute local directory
}

// New creates a Builder.
func New(cfg *config.Config) *Builder {
	return &Builder{
		cfg:    cfg,
		memo:   make(map[string]llb.State),
		mounts: make(map[string]string),
	}
}

// LocalMounts returns the local mount name -> directory map gathered during State().
// The keys match the llb.Local names referenced in the generated graph.
func (b *Builder) LocalMounts() map[string]string {
	return b.mounts
}

// State returns the llb.State for the task named name.
//
//   - cmds run in order, each on top of the previous command's root filesystem.
//   - source (if set) is mounted read-only at the working directory for every command.
//   - caches are mounted as shared persistent cache directories for every command.
//   - dependencies are mounted read-only into the first command to express
//     "must complete first" as an ordering edge (no data is shared).
func (b *Builder) State(name string) (llb.State, error) {
	if s, ok := b.memo[name]; ok {
		return s, nil
	}

	t := b.cfg.Tasks[name]
	// WithMetaResolver applies the image config (PATH and other ENV, etc.) so that, e.g.,
	// `go` on the golang image's PATH is found. Without it llb.Image only takes the rootfs.
	st := llb.Image(t.Image, llb.WithMetaResolver(imagemetaresolver.Default()))

	workdir := t.Dir
	if workdir == "" && t.Source != "" {
		workdir = defaultWorkdir
	}
	if workdir != "" {
		st = st.Dir(workdir)
	}
	for k, v := range t.Env {
		st = st.AddEnv(k, v)
	}

	// Register the local source as an llb.Local and remember the directory to mount.
	var srcState llb.State
	if t.Source != "" {
		mountName, err := b.registerSource(t.Source)
		if err != nil {
			return llb.State{}, err
		}
		srcState = llb.Local(mountName,
			llb.SharedKeyHint(mountName),
			llb.ExcludePatterns([]string{".git", "btr"}),
			llb.WithCustomNamef("local://%s", t.Source),
		)
	}

	for i, cmd := range t.Cmds {
		run := st.Run(llb.Shlex(cmd), llb.WithCustomNamef("[%s] %s", name, cmd))
		if t.Source != "" {
			run.AddMount(workdir, srcState, llb.Readonly)
		}
		for _, cachePath := range t.Caches {
			run.AddMount(cachePath, llb.Scratch(),
				llb.AsPersistentCacheDir(cachePath, llb.CacheMountShared))
		}
		if i == 0 { // mount deps on the first Run to create ordering edges
			for _, dep := range t.Deps {
				ds, err := b.State(dep)
				if err != nil {
					return llb.State{}, err
				}
				run.AddMount("/.btr/deps/"+dep, ds, llb.Readonly)
			}
		}
		st = run.Root()
	}

	b.memo[name] = st
	return st, nil
}

// registerSource records the local directory under a stable mount name (its cleaned
// path) so that identical sources across tasks share a single mount, and returns the name.
func (b *Builder) registerSource(source string) (string, error) {
	name := filepath.Clean(source)
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("failed to resolve source %q: %w", source, err)
	}
	b.mounts[name] = abs
	return name, nil
}
