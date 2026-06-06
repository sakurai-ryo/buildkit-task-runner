// Package graph resolves task dependencies and detects cycles.
package graph

import (
	"fmt"
	"sort"
	"strings"

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

// Mermaid renders the task dependency graph as a Mermaid flowchart.
// An edge "a --> b" means "a depends on b". If target is non-empty, only the tasks
// reachable from target are included (after a cycle/sanity check).
func Mermaid(cfg *config.Config, target string) (string, error) {
	var names []string
	if target != "" {
		if err := Resolve(cfg, target); err != nil {
			return "", err
		}
		reached := make(map[string]bool)
		var walk func(string)
		walk = func(name string) {
			if reached[name] {
				return
			}
			reached[name] = true
			for _, dep := range cfg.Tasks[name].Deps {
				walk(dep)
			}
		}
		walk(target)
		for name := range reached {
			names = append(names, name)
		}
	} else {
		for name := range cfg.Tasks {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %s\n", name)
	}
	for _, name := range names {
		deps := append([]string(nil), cfg.Tasks[name].Deps...)
		sort.Strings(deps)
		for _, dep := range deps {
			fmt.Fprintf(&b, "  %s --> %s\n", name, dep)
		}
	}
	return b.String(), nil
}
