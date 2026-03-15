# himate-public

Minimal public export of `himate`.

This repo keeps one idea in two single-file implementations:

- `agent.py`: Python `bash + Skill` coding agent
- `main.go`: Go `bash + Skill` coding agent

The repo stays intentionally small:

- no tests
- no multi-file provider layer
- no docs history
- no old prototypes

## Requirements

- Python 3.12+ with `uv`
- Go 1.22+
- `ANTHROPIC_AUTH_TOKEN`

Optional environment variables:

- `MODEL_NAME`
- `ANTHROPIC_BASE_URL`
- `MAX_TOKENS`
- `SKILLS_DIR`

## Bundled Skills

This repo now ships with two skills under `./skills`:

- `agent-builder`: design and build AI agents, capabilities, subagents, and skill systems
- `code-review`: structured code review for bugs, security, performance, and maintainability

## Skill Format

Both versions look for skills under `./skills` by default, or under `SKILLS_DIR` if set.
You can use the bundled skills as-is, and add your own alongside them.

Each skill should live in its own folder:

```text
skills/
  pdf/
    SKILL.md
```

Minimal `SKILL.md`:

```md
---
name: pdf
description: Process PDF files.
---

Use `pdftotext` for fast extraction.
Prefer `mutool draw -F txt` when layout matters.
```

Optional sibling folders such as `scripts/`, `references/`, and `assets/` are detected and listed to the model when the skill is loaded.

## Run

```bash
export ANTHROPIC_AUTH_TOKEN=your-token
export MODEL_NAME=claude-sonnet-4-20250514
```

Python one-shot:

```bash
uv run python agent.py "read docs from a PDF in this repo"
```

Python REPL:

```bash
uv run python agent.py
```

Go one-shot:

```bash
go run main.go "review the repo and use the right skill if needed"
```

Go REPL:

```bash
go run main.go
```

The model sees skill metadata in the system prompt and can load full skill content through the `Skill` tool. The code stays minimal; skills remain plain files you can inspect and extend.

The runtime now reads `ANTHROPIC_AUTH_TOKEN`. For compatibility, when you point at the default Anthropic endpoint it also mirrors that value to `x-api-key`, since the native Messages API docs still document that header.
