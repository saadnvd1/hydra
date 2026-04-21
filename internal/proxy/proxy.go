package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/creack/pty"
	"github.com/saadnvd1/hydra/internal/config"
	"github.com/saadnvd1/hydra/internal/logger"
	"github.com/saadnvd1/hydra/internal/session"
	"golang.org/x/term"
)

const (
	outputRingSize = 8192
	scanWindow     = 512
	switchByte     = 0x1d // Ctrl+]
)

// Regex to strip ALL ANSI/VT escape sequences
var ansiRegex = regexp.MustCompile(`\x1b` + `(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\].*?(?:\x1b\\|\x07)|\([AB012])`)

type Proxy struct {
	cfg        *config.Config
	currentIdx int
	startIdx   int
	extraArgs  []string
	log        *logger.Logger
	sess       *session.Session
	mu         sync.Mutex
	outputRing []byte
	limitHit   bool
	cmd        *exec.Cmd
	ptyFile    *os.File
}

func New(cfg *config.Config, startIdx int, extraArgs []string) *Proxy {
	return &Proxy{
		cfg:        cfg,
		extraArgs:  extraArgs,
		currentIdx: startIdx,
		startIdx:   startIdx,
		log:        logger.New(cfg.LogFile),
		sess:       &session.Session{},
	}
}

func WritePID() {
	dir := pidsDir()
	os.MkdirAll(dir, 0755)
	pid := fmt.Sprintf("%d", os.Getpid())
	os.WriteFile(filepath.Join(dir, pid), []byte(pid), 0644)
}

func CleanPID() {
	pid := fmt.Sprintf("%d", os.Getpid())
	os.Remove(filepath.Join(pidsDir(), pid))
}

// ReadAllPIDs returns all running ai process PIDs
func ReadAllPIDs() []int {
	dir := pidsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err == nil {
			// Verify process is still alive
			proc, err := os.FindProcess(pid)
			if err != nil {
				os.Remove(filepath.Join(dir, e.Name()))
				continue
			}
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				// Process dead, clean up stale PID file
				os.Remove(filepath.Join(dir, e.Name()))
				continue
			}
			pids = append(pids, pid)
		}
	}
	return pids
}

func pidsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "hydra", "pids")
}

func (p *Proxy) Run() {
	defer p.log.Close()

	WritePID()
	defer CleanPID()

	for p.currentIdx < len(p.cfg.Providers) {
		provider := p.cfg.Providers[p.currentIdx]
		p.log.Log("start_provider", "name", provider.Name, "index", fmt.Sprintf("%d", p.currentIdx))

		isFirst := (p.currentIdx == p.startIdx)
		exitCode := p.runProvider(provider, isFirst)

		if p.limitHit {
			p.sess.LastProvider = provider.Name
			p.sess.LimitHit = true
			p.sess.RecentOutput = string(p.outputRing)
			p.sess.Save()

			next := p.promptSwitch(provider.Name)
			if next == -1 {
				return
			}
			p.currentIdx = next
			p.limitHit = false

			nextProvider := p.cfg.Providers[p.currentIdx]
			fmt.Fprintf(os.Stderr, "\033[33m⟳ Switching to %s...\033[0m\n", nextProvider.Name)

			context := p.sess.BuildContinuationPrompt()
			if err := copyToClipboard(context); err == nil {
				fmt.Fprintf(os.Stderr, "\033[32m✓ Context copied to clipboard — paste it into the new session\033[0m\n\n")
			} else {
				fmt.Fprintf(os.Stderr, "\033[33mContext (copy manually):\033[0m\n%s\n", truncate(context, 2000))
			}

			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Normal exit
		p.sess.LastProvider = provider.Name
		p.sess.LimitHit = false
		p.sess.RecentOutput = string(p.outputRing)
		p.sess.Save()

		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}

	fmt.Fprintln(os.Stderr, "\n\033[31m✗ All providers exhausted.\033[0m")
	os.Exit(1)
}

func (p *Proxy) triggerSwitch() {
	p.mu.Lock()
	already := p.limitHit
	p.limitHit = true
	ptmx := p.ptyFile
	p.mu.Unlock()

	if already {
		return
	}

	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Signal(syscall.SIGTERM)
		go func() {
			time.Sleep(2 * time.Second)
			if p.cmd.ProcessState == nil {
				p.cmd.Process.Kill()
			}
			if ptmx != nil {
				ptmx.Close()
			}
		}()
	}
}

func (p *Proxy) runProvider(provider config.Provider, isFirst bool) int {
	args := make([]string, len(provider.Args))
	copy(args, provider.Args)
	if isFirst {
		args = append(args, p.extraArgs...)
	}
	cmd := exec.Command(provider.Command, args...)

	cmd.Env = os.Environ()
	for k, v := range provider.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\033[31mFailed to start %s: %v\033[0m\n", provider.Name, err)
		return 1
	}
	defer ptmx.Close()

	p.mu.Lock()
	p.cmd = cmd
	p.ptyFile = ptmx
	p.outputRing = nil
	p.mu.Unlock()

	// SIGUSR1 = switch provider (sent by `ai switch`)
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			p.triggerSwitch()
		}
	}()

	// SIGQUIT (Ctrl+\) = also switch
	sigquit := make(chan os.Signal, 1)
	signal.Notify(sigquit, syscall.SIGQUIT)
	go func() {
		for range sigquit {
			p.triggerSwitch()
		}
	}()

	// Window resize
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	_ = pty.InheritSize(os.Stdin, ptmx)

	// Raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set raw mode: %v\n", err)
		return 1
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	done := make(chan struct{}, 2)

	// stdin → pty (intercept Ctrl+])
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == switchByte {
					p.triggerSwitch()
					return
				}
			}
			if _, err := ptmx.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// pty → stdout (capture + scan)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
				p.appendOutput(buf[:n])
				p.scanForLimits()
			}
			if err != nil {
				return
			}
		}
	}()

	<-done

	signal.Stop(usr1)
	close(usr1)
	signal.Stop(sigquit)
	close(sigquit)
	signal.Stop(winch)
	close(winch)

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return exitCode
}

func (p *Proxy) appendOutput(data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.outputRing = append(p.outputRing, data...)
	if len(p.outputRing) > outputRingSize {
		p.outputRing = p.outputRing[len(p.outputRing)-outputRingSize:]
	}
}

func (p *Proxy) scanForLimits() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.limitHit {
		return
	}

	scanBytes := p.outputRing
	if len(scanBytes) > scanWindow {
		scanBytes = scanBytes[len(scanBytes)-scanWindow:]
	}

	cleaned := stripAnsi(string(scanBytes))
	lower := strings.ToLower(cleaned)

	for _, pattern := range p.cfg.LimitPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			p.limitHit = true
			p.log.Log("limit_detected", "pattern", pattern)

			go func() {
				time.Sleep(2 * time.Second)
				// Only show if process is still running (TUI apps)
				// For scripts that exit, we go straight to the menu
				if p.cmd != nil && p.cmd.ProcessState == nil {
					fmt.Fprintf(os.Stderr, "\n\033[33;1m⚡ Limit detected! Run `hydra switch` from another terminal to switch provider\033[0m\n")
				}
			}()
			return
		}
	}
}

func (p *Proxy) promptSwitch(currentProvider string) int {
	// Put terminal in raw mode for single-keypress menu
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Fallback to cooked mode
		return p.promptSwitchCooked(currentProvider)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	fmt.Fprintf(os.Stderr, "\r\n\033[33;1m━━━ Provider Switch ━━━\033[0m\r\n")
	fmt.Fprintf(os.Stderr, "\033[33m%s — switching. Choose next:\033[0m\r\n\r\n", currentProvider)

	available := []int{}
	for i, prov := range p.cfg.Providers {
		if i == p.currentIdx {
			continue
		}
		available = append(available, i)
		fmt.Fprintf(os.Stderr, "  \033[36m%d\033[0m) %s → %s\r\n", len(available), prov.Name, prov.Command)
	}
	fmt.Fprintf(os.Stderr, "  \033[36mq\033[0m) quit\r\n")
	fmt.Fprintf(os.Stderr, "\r\n\033[33mChoice: \033[0m")

	// Single keypress read in raw mode
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
	fmt.Fprintf(os.Stderr, "%c\r\n", buf[0])

	if buf[0] == 'q' || buf[0] == 'Q' {
		return -1
	}

	choice := int(buf[0] - '1')
	if choice >= 0 && choice < len(available) {
		return available[choice]
	}

	if len(available) > 0 {
		return available[0]
	}
	return -1
}

// Fallback if raw mode fails
func (p *Proxy) promptSwitchCooked(currentProvider string) int {
	fmt.Fprintf(os.Stderr, "\n\033[33;1m━━━ Provider Switch ━━━\033[0m\n")
	fmt.Fprintf(os.Stderr, "\033[33m%s — switching. Choose next:\033[0m\n\n", currentProvider)

	available := []int{}
	for i, prov := range p.cfg.Providers {
		if i == p.currentIdx {
			continue
		}
		available = append(available, i)
		fmt.Fprintf(os.Stderr, "  \033[36m%d\033[0m) %s → %s\n", len(available), prov.Name, prov.Command)
	}
	fmt.Fprintf(os.Stderr, "  \033[36mq\033[0m) quit\n")
	fmt.Fprintf(os.Stderr, "\n\033[33mChoice: \033[0m")

	buf := make([]byte, 16)
	n, _ := os.Stdin.Read(buf)
	if n == 0 {
		return -1
	}
	fmt.Fprintln(os.Stderr)

	if buf[0] == 'q' || buf[0] == 'Q' {
		return -1
	}

	choice := int(buf[0] - '1')
	if choice >= 0 && choice < len(available) {
		return available[choice]
	}

	if len(available) > 0 {
		return available[0]
	}
	return -1
}

// stripAnsi removes all ANSI escape sequences, control chars, and box-drawing garbage
func stripAnsi(s string) string {
	// Remove all ANSI/VT escape sequences
	s = ansiRegex.ReplaceAllString(s, "")

	// Remove remaining control characters (except newline, tab)
	var result strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || (r >= ' ' && !unicode.IsControl(r)) {
			result.WriteRune(r)
		}
	}

	// Collapse multiple blank lines
	lines := strings.Split(result.String(), "\n")
	var cleaned []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount <= 1 {
				cleaned = append(cleaned, "")
			}
		} else {
			blankCount = 0
			cleaned = append(cleaned, trimmed)
		}
	}

	return strings.Join(cleaned, "\n")
}

func copyToClipboard(text string) error {
	return clipboardCopy(text)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...(truncated)"
}
