package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ls1intum/momos/internal/llm"
	"github.com/ls1intum/momos/internal/protocol"
	"github.com/ls1intum/momos/internal/review"
)

// oneshot sends the diff and prompt in a single call and parses the response.
func (c *Config) oneshot(ctx context.Context, client *llm.Client, unified string) (*review.Review, llm.Usage, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: c.PromptText + "\n" + schemaInstruction},
		{Role: llm.RoleUser, Content: c.diffContext(unified)},
	}
	req := llm.Request{
		Model:          c.Model,
		Messages:       messages,
		MaxTokens:      c.MaxOutputTokens,
		Temperature:    0,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	var total llm.Usage
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return nil, total, err
	}
	addUsage(&total, resp.Usage)
	content := resp.Choices[0].Message.Content
	if rev, err := review.Parse([]byte(content)); err == nil {
		return rev, total, nil
	}
	// One repair retry (plan.md §11.1).
	messages = append(messages,
		llm.Message{Role: llm.RoleAssistant, Content: content},
		llm.Message{Role: llm.RoleUser, Content: "That was not valid review JSON. Output ONLY the JSON object matching the schema."},
	)
	req.Messages = messages
	resp2, err := client.Complete(ctx, req)
	if err != nil {
		return nil, total, err
	}
	addUsage(&total, resp2.Usage)
	rev, err := review.Parse([]byte(resp2.Choices[0].Message.Content))
	if err != nil {
		return nil, total, fmt.Errorf("model did not produce valid review json: %w", err)
	}
	return rev, total, nil
}

// agentic runs a bounded read-only agent loop, then finalizes to review.json.
func (c *Config) agentic(ctx context.Context, client *llm.Client, unified string) (*review.Review, llm.Usage, error) {
	var total llm.Usage
	tools := agentTools()
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: c.PromptText + "\n\nYou may explore the repository with the provided read-only tools before reviewing. When ready, respond with the final review JSON.\n" + schemaInstruction},
		{Role: llm.RoleUser, Content: c.diffContext(unified) + "\n\nExplore as needed, then produce the review JSON."},
	}

	for turn := 0; turn < c.MaxTurns; turn++ {
		req := llm.Request{
			Model:       c.Model,
			Messages:    messages,
			Tools:       tools,
			ToolChoice:  "auto",
			MaxTokens:   c.MaxOutputTokens,
			Temperature: 0,
		}
		resp, err := client.Complete(ctx, req)
		if err != nil {
			return nil, total, err
		}
		addUsage(&total, resp.Usage)
		msg := resp.Choices[0].Message

		if len(msg.ToolCalls) > 0 {
			messages = append(messages, msg)
			for _, tc := range msg.ToolCalls {
				result := executeTool(ctx, tc.Function.Name, tc.Function.Arguments)
				messages = append(messages, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    result,
				})
			}
			// Cost budget check after each turn (plan.md §11.3).
			if c.MaxCostUSD > 0 && c.cost(total) >= c.MaxCostUSD {
				break
			}
			continue
		}

		if rev, err := review.Parse([]byte(msg.Content)); err == nil {
			return rev, total, nil
		}
		// Not JSON and no tool call: nudge toward the final answer.
		messages = append(messages, msg, llm.Message{
			Role:    llm.RoleUser,
			Content: "Produce the final review as a single JSON object now, matching the schema.",
		})
	}

	// Turn/cost budget exhausted: force a final structured answer.
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "Stop exploring. Output the final review JSON now, matching the schema.",
	})
	req := llm.Request{
		Model:          c.Model,
		Messages:       messages,
		MaxTokens:      c.MaxOutputTokens,
		Temperature:    0,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return nil, total, err
	}
	addUsage(&total, resp.Usage)
	rev, err := review.Parse([]byte(resp.Choices[0].Message.Content))
	if err != nil {
		return nil, total, fmt.Errorf("agentic review produced no valid json: %w", err)
	}
	return rev, total, nil
}

func (c *Config) diffContext(unified string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nBase: %s\nHead: %s\n\nUnified diff:\n```diff\n%s\n```\n", c.RepoID, c.BaseSHA, c.HeadSHA, unified)
	return b.String()
}

func addUsage(total *llm.Usage, u llm.Usage) {
	total.PromptTokens += u.PromptTokens
	total.CompletionTokens += u.CompletionTokens
	total.TotalTokens += u.TotalTokens
}

// ---- Read-only agent tools ---------------------------------------------

func agentTools() []llm.Tool {
	strParam := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: "read_file", Description: "Read a file from the repository.", Parameters: obj(map[string]any{"path": strParam("repo-relative path")}, "path")}},
		{Type: "function", Function: llm.ToolFunction{Name: "list_dir", Description: "List a directory in the repository.", Parameters: obj(map[string]any{"path": strParam("repo-relative directory, '' for root")})}},
		{Type: "function", Function: llm.ToolFunction{Name: "grep", Description: "Search the repository for a regex pattern.", Parameters: obj(map[string]any{"pattern": strParam("regex"), "path": strParam("optional path to limit search")}, "pattern")}},
		{Type: "function", Function: llm.ToolFunction{Name: "git_show", Description: "Show a file at a revision (git show ref:path).", Parameters: obj(map[string]any{"ref": strParam("revision"), "path": strParam("repo-relative path")}, "ref", "path")}},
		{Type: "function", Function: llm.ToolFunction{Name: "git_log", Description: "Show recent commit history for a path.", Parameters: obj(map[string]any{"path": strParam("optional repo-relative path")})}},
	}
}

func executeTool(ctx context.Context, name, argsJSON string) string {
	var args map[string]string
	_ = json.Unmarshal([]byte(argsJSON), &args)
	switch name {
	case "read_file":
		p, err := safePath(args["path"])
		if err != nil {
			return "error: " + err.Error()
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "error: " + err.Error()
		}
		return truncate(string(b), 60000)
	case "list_dir":
		p, err := safePath(args["path"])
		if err != nil {
			return "error: " + err.Error()
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return "error: " + err.Error()
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name()+"/")
			} else {
				names = append(names, e.Name())
			}
		}
		return strings.Join(names, "\n")
	case "grep":
		out, _ := git(ctx, "grep", "-n", "-I", "-e", args["pattern"], "--", orAll(args["path"]))
		return truncate(out, 40000)
	case "git_show":
		out, err := git(ctx, "show", args["ref"]+":"+args["path"])
		if err != nil {
			return "error: " + err.Error()
		}
		return truncate(out, 60000)
	case "git_log":
		out, _ := git(ctx, "log", "--oneline", "-n", "20", "--", orAll(args["path"]))
		return truncate(out, 20000)
	default:
		return "error: unknown tool " + name
	}
}

// safePath resolves a repo-relative path and rejects escapes from the repo root
// (traversal, absolute paths). It rejects rather than clamps so injected tool
// arguments cannot smuggle a path out of the repository (plan.md §12.4).
func safePath(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository")
	}
	full := filepath.Join(protocol.RepoDir, clean)
	if full != protocol.RepoDir && !strings.HasPrefix(full, protocol.RepoDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository")
	}
	return full, nil
}

func orAll(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}
