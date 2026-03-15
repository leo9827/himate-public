package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type messageRequest struct {
	Model     string           `json:"model"`
	System    string           `json:"system"`
	MaxTokens int              `json:"max_tokens"`
	Tools     []toolDefinition `json:"tools"`
	Messages  []message        `json:"messages"`
}

type messageResponse struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
}

type skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
}

type skillLoader struct {
	dir    string
	skills map[string]skill
}

var (
	workdir    = mustGetwd()
	skillsDir  = getenv("SKILLS_DIR", filepath.Join(workdir, "skills"))
	baseURL    = strings.TrimRight(getenv("ANTHROPIC_BASE_URL", defaultBaseURL), "/")
	modelName  = getenv("MODEL_NAME", "claude-sonnet-4-20250514")
	maxTokens  = getenvInt("MAX_TOKENS", 4096)
	httpClient = &http.Client{Timeout: 5 * time.Minute}
	skills     = newSkillLoader(skillsDir)
)

const defaultBaseURL = "https://api.anthropic.com"

func main() {
	if len(os.Args) > 1 {
		answer, _, err := run(strings.Join(os.Args[1:], " "), nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(answer)
		return
	}

	session := []message(nil)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(">> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println()
			return
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" || prompt == "exit" || prompt == "quit" {
			return
		}
		answer, nextSession, err := run(prompt, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		session = nextSession
		fmt.Println(answer)
	}
}

func run(prompt string, history []message) (string, []message, error) {
	messages := append([]message(nil), history...)
	messages = append(messages, message{
		Role: "user",
		Content: []contentBlock{
			{Type: "text", Text: prompt},
		},
	})

	for {
		response, err := callAPI(messages)
		if err != nil {
			return "", messages, err
		}

		messages = append(messages, message{
			Role:    "assistant",
			Content: response.Content,
		})

		if response.StopReason != "tool_use" {
			return collectText(response.Content), messages, nil
		}

		results := make([]contentBlock, 0, len(response.Content))
		for _, block := range response.Content {
			if block.Type != "tool_use" {
				continue
			}
			output, isError := executeTool(block)
			results = append(results, contentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   output,
				IsError:   isError,
			})
		}
		if len(results) == 0 {
			return "", messages, fmt.Errorf("tool_use returned without tool blocks")
		}

		messages = append(messages, message{
			Role:    "user",
			Content: results,
		})
	}
}

func callAPI(messages []message) (messageResponse, error) {
	rawToken, bearerToken, err := authToken()
	if err != nil {
		return messageResponse{}, err
	}

	skills.Refresh()

	payload, err := json.Marshal(messageRequest{
		Model:     modelName,
		System:    buildSystem(),
		MaxTokens: maxTokens,
		Tools:     buildTools(),
		Messages:  messages,
	})
	if err != nil {
		return messageResponse{}, err
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return messageResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", bearerToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	if baseURL == defaultBaseURL {
		req.Header.Set("x-api-key", rawToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return messageResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return messageResponse{}, err
	}
	if resp.StatusCode >= 300 {
		return messageResponse{}, fmt.Errorf("API error %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var response messageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return messageResponse{}, err
	}
	return response, nil
}

func authToken() (string, string, error) {
	token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	if token == "" {
		return "", "", fmt.Errorf("ANTHROPIC_AUTH_TOKEN is required")
	}
	raw := token
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		raw = strings.TrimSpace(token[7:])
	}
	bearer := token
	if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
		bearer = "Bearer " + token
	}
	return raw, bearer, nil
}

func runBash(command string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = workdir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return formatToolResult(command, -1, "", "", "timeout after 60s"), true
	}

	exitCode := 0
	isError := false
	if err != nil {
		isError = true
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			if stderr.Len() > 0 && !strings.HasSuffix(stderr.String(), "\n") {
				stderr.WriteByte('\n')
			}
			stderr.WriteString(err.Error())
		}
	}

	return formatToolResult(command, exitCode, stdout.String(), stderr.String(), ""), isError
}

func runSkill(name string) (string, bool) {
	content, ok := skills.Content(name)
	if !ok {
		names := strings.Join(skills.Names(), ", ")
		if names == "" {
			names = "none"
		}
		return fmt.Sprintf("error: unknown skill %q. available: %s", name, names), true
	}
	return fmt.Sprintf(
		"<skill-loaded name=%q>\n%s\n</skill-loaded>\n\nFollow the skill above for the current task.",
		name,
		content,
	), false
}

func executeTool(block contentBlock) (string, bool) {
	switch block.Name {
	case "bash":
		return runBash(stringArg(block.Input, "command"))
	case "Skill":
		return runSkill(stringArg(block.Input, "skill"))
	default:
		return fmt.Sprintf("error: unknown tool %q", block.Name), true
	}
}

func buildSystem() string {
	return fmt.Sprintf(
		"You are a coding agent at %s.\n\nAvailable skills:\n%s\n\nRules:\n- If the task matches a skill, load it with the Skill tool before using bash.\n- Use bash for inspection and changes.\n- Keep the final answer brief.",
		workdir,
		skills.Descriptions(),
	)
}

func buildTools() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "bash",
			Description: "Execute a bash command.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "Skill",
			Description: "Load a skill when a task matches one of these descriptions:\n" + skills.Descriptions(),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skill": map[string]any{
						"type":        "string",
						"description": "The skill name to load.",
					},
				},
				"required": []string{"skill"},
			},
		},
	}
}

func formatToolResult(command string, exitCode int, stdout string, stderr string, errorText string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "command: %s\n", command)
	fmt.Fprintf(&builder, "exit_code: %d\n", exitCode)
	if errorText != "" {
		fmt.Fprintf(&builder, "error: %s\n", errorText)
	}
	builder.WriteString("stdout:\n")
	builder.WriteString(stdout)
	if !strings.HasSuffix(stdout, "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString("stderr:\n")
	builder.WriteString(stderr)
	if !strings.HasSuffix(stderr, "\n") {
		builder.WriteString("\n")
	}
	text := builder.String()
	if len(text) > 50000 {
		return text[:50000]
	}
	return text
}

func collectText(blocks []contentBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func newSkillLoader(dir string) *skillLoader {
	loader := &skillLoader{dir: dir}
	loader.Refresh()
	return loader
}

func (l *skillLoader) Refresh() {
	l.skills = map[string]skill{}

	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(l.dir, entry.Name(), "SKILL.md")
		skill, ok := parseSkill(skillPath)
		if ok {
			l.skills[skill.Name] = skill
		}
	}
}

func parseSkill(path string) (skill, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return skill{}, false
	}

	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return skill{}, false
	}

	rest := text[4:]
	index := strings.Index(rest, "\n---\n")
	if index < 0 {
		return skill{}, false
	}

	frontmatter := rest[:index]
	content := strings.TrimSpace(rest[index+5:])
	metadata := map[string]string{}
	for _, line := range strings.Split(frontmatter, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		metadata[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}

	name := metadata["name"]
	description := metadata["description"]
	if name == "" || description == "" {
		return skill{}, false
	}

	return skill{
		Name:        name,
		Description: description,
		Body:        content,
		Dir:         filepath.Dir(path),
	}, true
}

func (l *skillLoader) Descriptions() string {
	if len(l.skills) == 0 {
		return "(none)"
	}
	names := l.Names()
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("- %s: %s", name, l.skills[name].Description))
	}
	return strings.Join(lines, "\n")
}

func (l *skillLoader) Names() []string {
	names := make([]string, 0, len(l.skills))
	for name := range l.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (l *skillLoader) Content(name string) (string, bool) {
	skill, ok := l.skills[name]
	if !ok {
		return "", false
	}

	lines := []string{
		fmt.Sprintf("# Skill: %s", skill.Name),
		"",
		skill.Body,
	}

	resources := []string{}
	for _, folder := range []string{"scripts", "references", "assets"} {
		resourceDir := filepath.Join(skill.Dir, folder)
		entries, err := os.ReadDir(resourceDir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		if len(names) > 0 {
			resources = append(resources, fmt.Sprintf("%s: %s", folder, strings.Join(names, ", ")))
		}
	}

	if len(resources) > 0 {
		lines = append(lines, "", "Available resources:")
		for _, resource := range resources {
			lines = append(lines, "- "+resource)
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

func stringArg(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func getenv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return dir
}
