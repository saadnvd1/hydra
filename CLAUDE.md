# ai-cli

Go CLI. PTY-based wrapper around AI coding CLIs with automatic limit detection and provider switching.

## Structure
- `main.go` — entry point, arg routing, `ai switch` (SIGUSR1 to all PIDs), clipboard helper
- `internal/config/` — YAML config loading, default limit patterns from real CLI source
- `internal/proxy/` — PTY proxy: spawns CLI in pseudo-terminal, monitors output, SIGUSR1/SIGQUIT signal handling, provider picker menu (raw mode single-keypress), multi-PID management
- `internal/session/` — session persistence, Claude Code JSONL session file extraction (`~/.claude/projects/<project>/<id>.jsonl`), git diff/log capture, continuation prompt builder
- `internal/logger/` — append-only log file
- `test/` — fake provider scripts for testing limit detection flow

## Build
```bash
go build -o /usr/local/bin/ai .
```

## Config
`~/.config/ai-cli/config.yaml` — provider chain, permission bypass args/env, limit patterns.

## Key Design
- Full PTY passthrough — user sees actual TUI, no filtering
- Output ring buffer (8KB) scanned for limit patterns (ANSI-stripped via regex)
- `ai switch` sends SIGUSR1 to ALL running `ai` processes via PID files in `~/.local/share/ai-cli/pids/`
- Context extraction reads Claude Code JSONL session files directly, parses user/assistant messages
- Extra CLI args only forwarded to initial provider, not fallbacks (prevents flag leakage)
- Provider picker uses raw mode for single-keypress selection
- Permission bypass baked into config per provider (different mechanisms: CLI flags for Claude/Codex, env var for OpenCode, none needed for Pi)

## Provider-Specific Notes
- **Claude Code**: `--dangerously-skip-permissions` flag. Sessions stored as JSONL at `~/.claude/projects/`
- **OpenCode**: `OPENCODE_PERMISSION='{"*":"allow"}'` env var (TUI doesn't support `--dangerously-skip-permissions`, only `opencode run` does). Model format: `provider/model` (e.g. `google/gemini-2.5-flash`)
- **Codex**: `--full-auto` flag. Model format: just model name (e.g. `o3`)
- **Pi**: No permission system. Model format: pattern match without provider prefix (e.g. `gemini-2.5-flash`). Binary requires full nvm path in config due to lazy-loaded nvm.

## Free API Keys
- `GOOGLE_GENERATIVE_AI_API_KEY` — for OpenCode's Gemini models
- `GEMINI_API_KEY` — for Pi's Gemini models
- `GROQ_API_KEY` — for OpenCode's Groq models
- Set in `~/.zshenv`
