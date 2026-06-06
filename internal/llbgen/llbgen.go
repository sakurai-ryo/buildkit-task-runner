// Package llbgen converts task definitions into a BuildKit LLB state graph.
package llbgen

import (
	"github.com/moby/buildkit/client/llb"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/config"
)

// Builder memoizes task name -> llb.State conversions so shared deps are built only once.
type Builder struct {
	cfg  *config.Config
	memo map[string]llb.State
}

// New creates a Builder.
func New(cfg *config.Config) *Builder {
	return &Builder{cfg: cfg, memo: make(map[string]llb.State)}
}

// State returns the llb.State for the task named name.
// Dependencies are mounted read-only into the first Run to express "must complete first"
// as an ordering edge (no data is shared).
func (b *Builder) State(name string) (llb.State, error) {
	if s, ok := b.memo[name]; ok {
		return s, nil
	}

	t := b.cfg.Tasks[name]
	st := llb.Image(t.Image)
	if t.Dir != "" {
		st = st.Dir(t.Dir)
	}
	for k, v := range t.Env {
		st = st.AddEnv(k, v)
	}

	for i, cmd := range t.Cmds {
		run := st.Run(llb.Shlex(cmd), llb.WithCustomNamef("[%s] %s", name, cmd))
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
