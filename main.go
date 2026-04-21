package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/saadnvd1/hydra/internal/config"
	"github.com/saadnvd1/hydra/internal/proxy"
	"github.com/saadnvd1/hydra/internal/session"
)

const version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		cfg := loadConfig()
		runWithProvider(cfg, "")
		return
	}

	cmd := os.Args[1]

	switch cmd {
	case "--version", "-v":
		fmt.Println("hydra", version)
	case "--help", "-h":
		printUsage()
	case "--continue", "-c", "continue":
		runContinue()
	case "switch":
		runSwitch()
	case "config":
		runConfigInfo()
	case "status":
		runStatus()
	default:
		cfg := loadConfig()
		// Check if first arg matches a provider name
		for _, p := range cfg.Providers {
			if p.Name == cmd {
				runWithProvider(cfg, p.Name, os.Args[2:]...)
				return
			}
		}
		// Otherwise start primary provider, pass ALL args through
		runWithProvider(cfg, "", os.Args[1:]...)
	}
}

func runWithProvider(cfg *config.Config, providerName string, extraArgs ...string) {
	startIdx := 0
	if providerName != "" {
		for i, p := range cfg.Providers {
			if p.Name == providerName {
				startIdx = i
				break
			}
		}
	}

	p := proxy.New(cfg, startIdx, extraArgs)
	p.Run()
}

func runContinue() {
	cfg := loadConfig()
	sess, err := session.LoadLast()
	if err != nil {
		// No previous session, just start fresh
		runWithProvider(cfg, "")
		return
	}

	fmt.Fprintf(os.Stderr, "\033[33mResuming with context from previous session\033[0m\n")
	fmt.Fprintf(os.Stderr, "Last provider: %s\n\n", sess.LastProvider)

	// Find next provider after the last one used
	startIdx := 0
	for i, p := range cfg.Providers {
		if p.Name == sess.LastProvider {
			startIdx = i + 1
			break
		}
	}
	if startIdx >= len(cfg.Providers) {
		startIdx = 0
	}

	// Copy context to clipboard
	context := sess.BuildContinuationPrompt()
	if err := copyToClipboard(context); err == nil {
		fmt.Fprintf(os.Stderr, "\033[32m✓ Context copied to clipboard\033[0m\n\n")
	}

	p := proxy.New(cfg, startIdx, nil)
	p.Run()
}

func runSwitch() {
	pids := proxy.ReadAllPIDs()
	if len(pids) == 0 {
		fmt.Fprintln(os.Stderr, "No running hydra sessions found.")
		os.Exit(1)
	}

	signaled := 0
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.SIGUSR1); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to signal pid %d: %v\n", pid, err)
			continue
		}
		signaled++
	}

	fmt.Fprintf(os.Stderr, "Sent switch signal to %d hydra session(s)\n", signaled)
}

func runStatus() {
	sess, err := session.LoadLast()
	if err != nil {
		fmt.Fprintln(os.Stderr, "No previous session.")
		os.Exit(1)
	}
	fmt.Printf("Last provider:  %s\n", sess.LastProvider)
	fmt.Printf("Limit hit:      %v\n", sess.LimitHit)
	if sess.RecentOutput != "" {
		lines := strings.Split(sess.RecentOutput, "\n")
		show := lines
		if len(show) > 10 {
			show = show[len(show)-10:]
		}
		fmt.Printf("Recent output:\n%s\n", strings.Join(show, "\n"))
	}
}

func runConfigInfo() {
	cfg := loadConfig()
	fmt.Printf("Config: %s\n\n", cfg.Path)
	fmt.Printf("Providers (%d):\n", len(cfg.Providers))
	for i, p := range cfg.Providers {
		role := "primary"
		if i > 0 {
			role = fmt.Sprintf("fallback-%d", i)
		}
		fmt.Printf("  [%s] %s → %s %s\n", role, p.Name, p.Command, strings.Join(p.Args, " "))
	}
	fmt.Printf("\nSwitch key: %s\n", cfg.SwitchKey)
	fmt.Printf("Limit patterns: %d configured\n", len(cfg.LimitPatterns))
}

func loadConfig() *config.Config {
	path := os.Getenv("HYDRA_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "hydra", "config.yaml")
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error (%s): %v\n", path, err)
		os.Exit(1)
	}
	return cfg
}

func copyToClipboard(text string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip.exe")
	default:
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		}
	}

	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func printUsage() {
	fmt.Println(`hydra - unified AI coding CLI with automatic fallback

Usage:
  hydra                        Start with primary provider (interactive)
  hydra <provider>             Start with specific provider (e.g. hydra codex)
  hydra switch                 Switch provider (run from another terminal)
  hydra --continue, -c         Resume from last session with context
  hydra config                 Show config
  hydra status                 Show last session

To switch providers while a session is running:
  Open another terminal and run: hydra switch

When a usage limit is detected automatically, you'll also be prompted.
Context is captured and copied to your clipboard on switch.

Config: ~/.config/hydra/config.yaml`)
}
