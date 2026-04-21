package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Provider struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

type Config struct {
	Providers     []Provider `yaml:"providers"`
	LimitPatterns []string   `yaml:"limit_patterns"`
	SwitchKey     string     `yaml:"switch_key"` // key combo description shown to user
	LogFile       string     `yaml:"log_file"`
	Path          string     `yaml:"-"`
}

// Default limit patterns from real CLI source code
var defaultLimitPatterns = []string{
	// Claude Code
	"rate limit",
	"credit balance is too low",
	"overloaded",
	"high load",
	"usage limit",
	// OpenCode
	"free usage exceeded",
	"insufficient_quota",
	"too many requests",
	"provider is overloaded",
	// Codex
	"quota exceeded",
	"at capacity",
	"high demand",
	"context window exceeded",
	// Pi
	"overloaded_error",
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid yaml: %w", err)
	}

	cfg.Path = path

	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	if len(cfg.LimitPatterns) == 0 {
		cfg.LimitPatterns = defaultLimitPatterns
	}

	if cfg.SwitchKey == "" {
		cfg.SwitchKey = "Ctrl+]"
	}

	if cfg.LogFile == "" {
		home, _ := os.UserHomeDir()
		cfg.LogFile = home + "/.local/share/hydra/sessions.log"
	}

	return &cfg, nil
}
