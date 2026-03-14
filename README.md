# himate-public

Minimal public export of `himate`.

This repo keeps one idea in two single-file implementations:

- `agent.py`: Python bash-only coding agent
- `main.go`: Go bash-only coding agent

What is intentionally not carried over:

- `docs/`
- `cr/`
- tests
- skills
- multi-file provider abstractions
- old prototypes
- original git history

## Requirements

- Python 3.12+ with `uv`
- Go 1.22+
- `ANTHROPIC_API_KEY`

Optional environment variables:

- `MODEL_NAME`
- `ANTHROPIC_BASE_URL`
- `MAX_TOKENS`

## Run

```bash
export ANTHROPIC_API_KEY=your-key
export MODEL_NAME=claude-sonnet-4-20250514
```

Python one-shot:

```bash
uv run python agent.py "scan this repository and summarize it"
```

Python REPL:

```bash
uv run python agent.py
```

Go one-shot:

```bash
go run main.go "scan this repository and summarize it"
```

Go REPL:

```bash
go run main.go
```

Both versions use the Anthropic Messages API with a single `bash` tool. They are intentionally small and do not include streaming, retries, sandboxing, permission gates, or tests.
