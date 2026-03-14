# himate-public

Minimal public export of `himate`.

This repo keeps one idea in two single-file implementations:

- `agent.py`: Python `bash + Skill` coding agent
- `main.go`: Go `bash + Skill` coding agent

The repo stays intentionally small:

- no bundled skills
- no tests
- no multi-file provider layer
- no docs history
- no old prototypes

## Requirements

- Python 3.12+ with `uv`
- Go 1.22+
- `ANTHROPIC_API_KEY`

Optional environment variables:

- `MODEL_NAME`
- `ANTHROPIC_BASE_URL`
- `MAX_TOKENS`
- `SKILLS_DIR`

## Skill Format

Both versions look for skills under `./skills` by default, or under `SKILLS_DIR` if set.

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
export ANTHROPIC_API_KEY=your-key
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

The model sees skill metadata in the system prompt and can load full skill content through the `Skill` tool. No sample skills are bundled here; code stays minimal, skills stay external.
