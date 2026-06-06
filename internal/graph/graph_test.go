package graph

import (
	"strings"
	"testing"

	"github.com/sakurai-ryo/buildkit-task-runner/internal/config"
)

func cfg(tasks map[string]config.Task) *config.Config {
	return &config.Config{Tasks: tasks}
}

func TestResolveOK(t *testing.T) {
	c := cfg(map[string]config.Task{
		"deps":  {},
		"build": {Deps: []string{"deps"}},
		"lint":  {Deps: []string{"deps"}},
		"all":   {Deps: []string{"build", "lint"}},
	})
	if err := Resolve(c, "all"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestResolveUnknownTarget(t *testing.T) {
	c := cfg(map[string]config.Task{"a": {}})
	if err := Resolve(c, "missing"); err == nil {
		t.Fatal("expected error for unknown target")
	}
}

func TestResolveCycle(t *testing.T) {
	c := cfg(map[string]config.Task{
		"a": {Deps: []string{"b"}},
		"b": {Deps: []string{"a"}},
	})
	err := Resolve(c, "a")
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected cyclic dependency error, got %v", err)
	}
}
