package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var resumeIDRegex = regexp.MustCompile(`claude\s+--resume\s+([0-9a-f-]{36})`)

type Session struct {
	LastProvider string `json:"last_provider"`
	LimitHit     bool   `json:"limit_hit"`
	RecentOutput string `json:"recent_output"`
	ResumeID     string `json:"resume_id,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Timestamp    string `json:"timestamp"`
}

func (s *Session) ExtractResumeID() {
	if s.RecentOutput == "" {
		return
	}
	matches := resumeIDRegex.FindStringSubmatch(s.RecentOutput)
	if len(matches) >= 2 {
		s.ResumeID = matches[1]
	}
}

func (s *Session) BuildContinuationPrompt() string {
	var b strings.Builder

	b.WriteString("I was working on a task in another AI coding CLI (")
	b.WriteString(s.LastProvider)
	b.WriteString(") but hit a usage limit. Please continue where it left off.\n\n")

	// Extract conversation from Claude Code session file
	if s.ResumeID != "" {
		convo := extractClaudeConversation(s.ResumeID, s.Cwd)
		if convo != "" {
			b.WriteString("## Conversation So Far\n")
			b.WriteString(convo)
			b.WriteString("\n\n")
		}
	}

	// Git diff
	diff := captureGitDiff()
	if diff != "" {
		b.WriteString("## Uncommitted Changes\n")
		b.WriteString("```diff\n")
		if len(diff) > 4000 {
			b.WriteString(diff[:4000])
			b.WriteString("\n... (truncated)\n")
		} else {
			b.WriteString(diff)
		}
		b.WriteString("```\n\n")
	}

	// Recent commits
	recentLog := captureGitLog()
	if recentLog != "" {
		b.WriteString("## Recent Commits\n")
		b.WriteString("```\n")
		b.WriteString(recentLog)
		b.WriteString("```\n\n")
	}

	b.WriteString("## Instructions\n")
	b.WriteString("Continue the task from where the previous tool stopped. Don't redo completed work.\n")

	return b.String()
}

// extractClaudeConversation reads a Claude Code session JSONL file and
// returns the conversation as clean text.
func extractClaudeConversation(sessionID string, cwd string) string {
	path := findSessionFile(sessionID, cwd)
	if path == "" {
		return ""
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		if entryType != "user" && entryType != "assistant" {
			continue
		}

		msg, ok := entry["message"].(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		text := extractMessageText(msg["content"])
		if text == "" {
			continue
		}

		// Truncate very long messages
		if len(text) > 2000 {
			text = text[:2000] + "\n... (truncated)"
		}

		if role == "user" {
			b.WriteString("**User:** ")
		} else {
			b.WriteString("**Assistant:** ")
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}

	result := b.String()
	// Keep only the last ~6000 chars if too long
	if len(result) > 6000 {
		result = result[len(result)-6000:]
		// Find first complete message boundary
		idx := strings.Index(result, "**User:**")
		if idx == -1 {
			idx = strings.Index(result, "**Assistant:**")
		}
		if idx > 0 {
			result = "... (earlier messages truncated)\n\n" + result[idx:]
		}
	}

	return strings.TrimSpace(result)
}

func extractMessageText(content interface{}) string {
	// String content
	if s, ok := content.(string); ok {
		return s
	}

	// Array of content blocks
	if blocks, ok := content.([]interface{}); ok {
		var parts []string
		for _, block := range blocks {
			if m, ok := block.(map[string]interface{}); ok {
				blockType, _ := m["type"].(string)
				if blockType == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				} else if blockType == "tool_use" {
					name, _ := m["name"].(string)
					parts = append(parts, fmt.Sprintf("[tool: %s]", name))
				} else if blockType == "tool_result" {
					// Skip tool results — too noisy
				}
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

// findSessionFile locates the JSONL file for a session ID
func findSessionFile(sessionID string, cwd string) string {
	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".claude", "projects")

	filename := sessionID + ".jsonl"

	// Try cwd-based project dir first
	if cwd != "" {
		sanitized := sanitizePath(cwd)
		candidate := filepath.Join(projectsDir, sanitized, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Search all project dirs
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, entry.Name(), filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

// sanitizePath matches Claude Code's path sanitization for project dirs
func sanitizePath(p string) string {
	var result strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}
	return result.String()
}

func captureGitDiff() string {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return ""
	}

	cmd = exec.Command("git", "diff", "HEAD")
	diff, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(diff))) == 0 {
		cmd = exec.Command("git", "diff")
		diff, err = cmd.Output()
		if err != nil || len(strings.TrimSpace(string(diff))) == 0 {
			cmd = exec.Command("git", "diff", "--cached")
			diff, _ = cmd.Output()
		}
	}

	return strings.TrimSpace(string(diff))
}

func captureGitLog() string {
	cmd := exec.Command("git", "log", "--oneline", "-5", "--no-decorate")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Session) Save() {
	s.Timestamp = time.Now().Format(time.RFC3339)
	s.ExtractResumeID()

	// Capture cwd for session file lookup
	if s.Cwd == "" {
		s.Cwd, _ = os.Getwd()
	}

	dir := sessionDir()
	os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}

	os.WriteFile(filepath.Join(dir, "last.json"), data, 0644)

	ts := time.Now().Format("2006-01-02T15-04-05")
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("session-%s.json", ts)), data, 0644)
}

func LoadLast() (*Session, error) {
	path := filepath.Join(sessionDir(), "last.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func sessionDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "hydra", "sessions")
}
