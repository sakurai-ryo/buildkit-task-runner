// Package graph resolves task dependencies and detects cycles.
package graph

import (
	"fmt"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/config"
)

// State for three-color marking.
type color int

const (
	white color = iota // unvisited
	gray               // visiting (on the current DFS path)
	black              // fully visited
)

// Resolve walks deps from target via DFS and detects undefined references and cycles.
// LLB generation uses recursion with memoization, so this only performs graph sanity checks.
func Resolve(cfg *config.Config, target string) error {
	if _, ok := cfg.Tasks[target]; !ok {
		return fmt.Errorf("task %q is not defined", target)
	}

	colors := make(map[string]color, len(cfg.Tasks))
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		switch colors[name] {
		case gray:
			return fmt.Errorf("cyclic dependency detected: %v", append(path, name))
		case black:
			return nil
		}
		colors[name] = gray
		path = append(path, name)
		for _, dep := range cfg.Tasks[name].Deps {
			if err := visit(dep, path); err != nil {
				return err
			}
		}
		colors[name] = black
		return nil
	}
	return visit(target, nil)
}
