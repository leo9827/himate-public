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
	"strings"
	"time"
)

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type toolInput struct {
	Command string `json:"command"`
}

type contentBlock struct {
	Type      string    `json:"type"`
	Text      string    `json:"text,omitempty"`
	ID        string    `json:"id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Input     toolInput `json:"input,omitempty"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	Content   string    `json:"content,omitempty"`
	IsError   bool      `json:"is_error,omitempty"`
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

var (
	workdir    = mustGetwd()
	baseURL    = strings.TrimRight(getenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com"), "/")
	modelName  = getenv("MODEL_NAME", "claude-sonnet-4-20250514")
	maxTokens  = getenvInt("MAX_TOKENS", 4096)
	systemText = fmt.Sprintf(
		"You are a coding agent at %s. Use bash when needed, inspect paths before assuming, and keep the final answer brief.",
		workdir,
	)
	tools = []toolDefinition{
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
	}
	httpClient = &http.Client{Timeout: 5 * time.Minute}
)

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
		Role:    "user",
		Content: []contentBlock{{Type: "text", Text: prompt}},
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
			output, isError := runBash(block.Input.Command)
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
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return messageResponse{}, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}

	payload, err := json.Marshal(messageRequest{
		Model:     modelName,
		System:    systemText,
		MaxTokens: maxTokens,
		Tools:     tools,
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
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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
