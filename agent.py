#!/usr/bin/env python3
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

WORKDIR = Path.cwd()
SKILLS_DIR = Path(os.getenv("SKILLS_DIR", str(WORKDIR / "skills")))
DEFAULT_BASE_URL = "https://api.anthropic.com"
BASE_URL = os.getenv("ANTHROPIC_BASE_URL", DEFAULT_BASE_URL).rstrip("/")
API_URL = f"{BASE_URL}/v1/messages"
MODEL = os.getenv("MODEL_NAME", "claude-sonnet-4-20250514")
MAX_TOKENS = int(os.getenv("MAX_TOKENS", "4096"))


class SkillLoader:
    def __init__(self, skills_dir):
        self.skills_dir = skills_dir
        self.skills = {}
        self.refresh()

    def refresh(self):
        self.skills = {}
        if not self.skills_dir.exists():
            return
        for child in sorted(self.skills_dir.iterdir()):
            if not child.is_dir():
                continue
            skill = self.parse(child / "SKILL.md")
            if skill:
                self.skills[skill["name"]] = skill

    def parse(self, path):
        if not path.exists():
            return None
        match = re.match(r"^---\s*\n(.*?)\n---\s*\n(.*)$", path.read_text(), re.DOTALL)
        if not match:
            return None
        frontmatter, body = match.groups()
        metadata = {}
        for line in frontmatter.splitlines():
            if ":" not in line:
                continue
            key, value = line.split(":", 1)
            metadata[key.strip()] = value.strip().strip("\"'")
        name = metadata.get("name")
        description = metadata.get("description")
        if not name or not description:
            return None
        return {
            "name": name,
            "description": description,
            "body": body.strip(),
            "dir": path.parent,
        }

    def descriptions(self):
        if not self.skills:
            return "(none)"
        return "\n".join(
            f"- {name}: {skill['description']}"
            for name, skill in sorted(self.skills.items())
        )

    def names(self):
        return sorted(self.skills)

    def content(self, name):
        skill = self.skills.get(name)
        if not skill:
            return None
        parts = [f'# Skill: {skill["name"]}', "", skill["body"]]
        resources = []
        for folder in ("scripts", "references", "assets"):
            folder_path = skill["dir"] / folder
            if not folder_path.exists():
                continue
            names = sorted(child.name for child in folder_path.iterdir())
            if names:
                resources.append(f"{folder}: {', '.join(names)}")
        if resources:
            parts.extend(["", "Available resources:"])
            parts.extend(f"- {item}" for item in resources)
        return "\n".join(parts).strip()


SKILLS = SkillLoader(SKILLS_DIR)


def build_system():
    return (
        f"You are a coding agent at {WORKDIR}.\n\n"
        "Available skills:\n"
        f"{SKILLS.descriptions()}\n\n"
        "Rules:\n"
        "- If the task matches a skill, load it with the Skill tool before using bash.\n"
        "- Use bash for inspection and changes.\n"
        "- Keep the final answer brief."
    )


def build_tools():
    return [
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
        },
        {
            "name": "Skill",
            "description": "Load a skill when a task matches one of these descriptions:\n"
            f"{SKILLS.descriptions()}",
            "input_schema": {
                "type": "object",
                "properties": {
                    "skill": {
                        "type": "string",
                        "description": "The skill name to load.",
                    }
                },
                "required": ["skill"],
            },
        },
    ]


def text_block(text):
    return {"type": "text", "text": text}


def auth_headers():
    token = os.getenv("ANTHROPIC_AUTH_TOKEN")
    if not token:
        raise RuntimeError("ANTHROPIC_AUTH_TOKEN is required")
    raw_token = token[7:] if token.lower().startswith("bearer ") else token
    bearer_token = token if token.lower().startswith("bearer ") else f"Bearer {token}"
    headers = {
        "content-type": "application/json",
        "authorization": bearer_token,
        "anthropic-version": "2023-06-01",
    }
    if BASE_URL == DEFAULT_BASE_URL:
        headers["x-api-key"] = raw_token
    return headers


def call_api(messages):
    SKILLS.refresh()
    payload = {
        "model": MODEL,
        "system": build_system(),
        "max_tokens": MAX_TOKENS,
        "tools": build_tools(),
        "messages": messages,
    }
    request = urllib.request.Request(
        API_URL,
        data=json.dumps(payload).encode(),
        method="POST",
        headers=auth_headers(),
    )
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"API error {exc.code}: {body}") from exc


def format_tool_result(command, exit_code, stdout, stderr, error_text=""):
    lines = [f"command: {command}", f"exit_code: {exit_code}"]
    if error_text:
        lines.append(f"error: {error_text}")
    lines.extend(["stdout:", stdout, "stderr:", stderr])
    return ("\n".join(lines).strip() + "\n")[:50000]


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


def run_skill(name):
    content = SKILLS.content(name)
    if content is None:
        names = ", ".join(SKILLS.names()) or "none"
        return f"error: unknown skill {name!r}. available: {names}", True
    return (
        f'<skill-loaded name="{name}">\n{content}\n</skill-loaded>\n\n'
        "Follow the skill above for the current task."
    ), False


def collect_text(content):
    return "".join(block.get("text", "") for block in content if block.get("type") == "text")


def execute_tool(block):
    name = block.get("name")
    data = block.get("input") or {}
    if name == "bash":
        return run_bash(str(data.get("command", "")))
    if name == "Skill":
        return run_skill(str(data.get("skill", "")))
    return f"error: unknown tool {name!r}", True


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
            output, is_error = execute_tool(block)
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
