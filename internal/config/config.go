// Package config loads and validates the task definition YAML.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config corresponds to the whole tasks.yaml.
type Config struct {
	Tasks map[string]Task `yaml:"tasks"`
}

// Task is a single task definition. It runs cmds in order on top of the given image.
type Task struct {
	Image  string            `yaml:"image"`
	Cmds   []string          `yaml:"cmds"`
	Deps   []string          `yaml:"deps"`
	Env    map[string]string `yaml:"env"`
	Dir    string            `yaml:"dir"`
	Source string            `yaml:"source"` // local directory mounted read-only into the container (defaults workdir to /src)
	Caches []string          `yaml:"caches"` // container paths backed by a shared persistent cache (e.g. /go/pkg/mod)
}

// Load reads the YAML at path and returns a validated Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read task definition: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if len(c.Tasks) == 0 {
		return fmt.Errorf("no tasks are defined")
	}
	for name, t := range c.Tasks {
		if t.Image == "" {
			return fmt.Errorf("task %q: image is required", name)
		}
		if len(t.Cmds) == 0 {
			return fmt.Errorf("task %q: at least one cmd is required", name)
		}
		for _, dep := range t.Deps {
			if _, ok := c.Tasks[dep]; !ok {
				return fmt.Errorf("task %q: references undefined dependency %q", name, dep)
			}
		}
	}
	return nil
}
