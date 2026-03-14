#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

BASE_URL = os.getenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com").rstrip("/")
API_URL = f"{BASE_URL}/v1/messages"
MODEL = os.getenv("MODEL_NAME", "claude-sonnet-4-20250514")
MAX_TOKENS = int(os.getenv("MAX_TOKENS", "4096"))
WORKDIR = Path.cwd()
SYSTEM = (
    f"You are a coding agent at {WORKDIR}. "
    "Use bash when needed, inspect paths before assuming, and keep the final answer brief."
)
TOOLS = [
    {
        "name": "bash",
        "description": "Execute a bash command.",
        "input_schema": {
            "type": "object",
            "properties": {
                "command": {
                    "type": "string",
                    "description": "The bash command to execute.",
                }
            },
            "required": ["command"],
        },
    }
]


def text_block(text):
    return {"type": "text", "text": text}


def call_api(messages):
    api_key = os.getenv("ANTHROPIC_API_KEY")
    if not api_key:
        raise RuntimeError("ANTHROPIC_API_KEY is required")
    payload = {
        "model": MODEL,
        "system": SYSTEM,
        "max_tokens": MAX_TOKENS,
        "tools": TOOLS,
        "messages": messages,
    }
    request = urllib.request.Request(
        API_URL,
        data=json.dumps(payload).encode(),
        method="POST",
        headers={
            "content-type": "application/json",
            "x-api-key": api_key,
            "anthropic-version": "2023-06-01",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"API error {exc.code}: {body}") from exc


def format_tool_result(command, exit_code, stdout, stderr, error_text=""):
    body = [
        f"command: {command}",
        f"exit_code: {exit_code}",
    ]
    if error_text:
        body.append(f"error: {error_text}")
    body.extend(["stdout:", stdout, "stderr:", stderr])
    text = "\n".join(body).strip() + "\n"
    return text[:50000]


def run_bash(command):
    try:
        result = subprocess.run(
            ["bash", "-lc", command],
            cwd=WORKDIR,
            capture_output=True,
            text=True,
            timeout=60,
        )
    except subprocess.TimeoutExpired:
        return format_tool_result(command, -1, "", "", "timeout after 60s"), True
    return (
        format_tool_result(command, result.returncode, result.stdout, result.stderr),
        result.returncode != 0,
    )


def collect_text(content):
    return "".join(block.get("text", "") for block in content if block.get("type") == "text")


def run(prompt, history=None):
    messages = list(history or [])
    messages.append({"role": "user", "content": [text_block(prompt)]})
    while True:
        response = call_api(messages)
        content = response.get("content", [])
        messages.append({"role": "assistant", "content": content})
        if response.get("stop_reason") != "tool_use":
            return collect_text(content), messages
        results = []
        for block in content:
            if block.get("type") != "tool_use":
                continue
            command = block.get("input", {}).get("command", "")
            output, is_error = run_bash(command)
            results.append(
                {
                    "type": "tool_result",
                    "tool_use_id": block["id"],
                    "content": output,
                    "is_error": is_error,
                }
            )
        if not results:
            raise RuntimeError("tool_use returned without tool blocks")
        messages.append({"role": "user", "content": results})


def main():
    if len(sys.argv) > 1:
        answer, _ = run(" ".join(sys.argv[1:]))
        print(answer)
        return
    history = []
    while True:
        try:
            prompt = input(">> ").strip()
        except (EOFError, KeyboardInterrupt):
            print()
            return
        if prompt in {"", "exit", "quit"}:
            return
        try:
            answer, history = run(prompt, history)
        except Exception as exc:
            print(f"error: {exc}", file=sys.stderr)
            continue
        print(answer)


if __name__ == "__main__":
    main()
