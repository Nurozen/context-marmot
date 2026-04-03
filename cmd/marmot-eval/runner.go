package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runVanilla executes a question against a real repo clone using standard file tools.
func runVanilla(question, repoDir, model string) (RunResult, error) {
	prompt := fmt.Sprintf(
		"<role>You are answering a code comprehension question about a Python repository.</role>\n\n"+
			"<guidelines>\n"+
			"- Use Read, Grep, Glob, and Bash to explore the code.\n"+
			"- Reference specific files, functions, and line numbers in your answer.\n"+
			"- Be thorough but efficient — find the relevant code and explain it.\n"+
			"</guidelines>\n\n"+
			"<question>%s</question>", question)

	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--tools", "Read,Grep,Glob,Bash",
		"--add-dir", repoDir,
		"--max-budget-usd", "0.50",
	}

	return invokeClaude(args, prompt)
}

// runMCP executes a question using only ContextMarmot MCP tools.
func runMCP(question, marmotBinary, vaultDir, model string) (RunResult, error) {
	// Write temporary MCP config.
	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{
				"command": marmotBinary,
				"args":    []string{"serve", "--dir", vaultDir},
			},
		},
	}
	configBytes, err := json.Marshal(mcpConfig)
	if err != nil {
		return RunResult{}, fmt.Errorf("marshal mcp config: %w", err)
	}

	prompt := fmt.Sprintf(
		"<role>You are answering a code comprehension question about a Python repository using a knowledge graph.</role>\n\n"+
			"<guidelines>\n"+
			"- Use context_query to search the knowledge graph for relevant context.\n"+
			"- You may issue multiple queries with different search terms if the first result is insufficient.\n"+
			"- Answer thoroughly, referencing the specific nodes and relationships returned by the graph.\n"+
			"</guidelines>\n\n"+
			"<question>%s</question>", question)

	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--tools", "",
		"--allowedTools", "mcp__context-marmot__context_query,mcp__context-marmot__context_verify",
		"--mcp-config", string(configBytes),
		"--strict-mcp-config",
		"--max-budget-usd", "0.50",
	}

	return invokeClaude(args, prompt)
}

// runHybrid executes a question with both ContextMarmot MCP tools AND file tools.
// The graph provides a map of the codebase; file tools provide actual source code.
func runHybrid(question, repoDir, marmotBinary, vaultDir, model string) (RunResult, error) {
	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{
				"command": marmotBinary,
				"args":    []string{"serve", "--dir", vaultDir},
			},
		},
	}
	configBytes, err := json.Marshal(mcpConfig)
	if err != nil {
		return RunResult{}, fmt.Errorf("marshal mcp config: %w", err)
	}

	prompt := fmt.Sprintf(
		"<role>You are answering a code comprehension question about a Python repository.</role>\n\n"+
			"<workflow>\n"+
			"<step>Call context_query with the question to get a map of relevant files, functions, and relationships.</step>\n"+
			"<step>Read ONLY the specific files and line ranges mentioned in the graph result. Do not explore broadly.</step>\n"+
			"<step>If the graph result is insufficient, use Grep to search for specific identifiers it mentioned.</step>\n"+
			"<step>Answer based on what you found. Cite specific files, functions, and line numbers.</step>\n"+
			"</workflow>\n\n"+
			"<guidelines>\n"+
			"- The knowledge graph already maps the codebase architecture. Trust it to point you to the right files.\n"+
			"- Do NOT use Glob to scan directories or Read files speculatively. Only read what the graph tells you to.\n"+
			"- Your job is to read the actual source at the locations the graph identifies, then provide a detailed answer.\n"+
			"</guidelines>\n\n"+
			"<question>%s</question>", question)

	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--tools", "Read,Grep,Glob,Bash",
		"--allowedTools", "mcp__context-marmot__context_query,mcp__context-marmot__context_verify",
		"--mcp-config", string(configBytes),
		"--strict-mcp-config",
		"--add-dir", repoDir,
		"--max-budget-usd", "0.50",
	}

	return invokeClaude(args, prompt)
}

// invokeClaude runs the claude CLI and parses the JSON output.
func invokeClaude(args []string, prompt string) (RunResult, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return RunResult{}, fmt.Errorf("claude CLI not found: %w", err)
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(os.Args[0]) // working dir doesn't matter much

	out, err := cmd.Output()
	if err != nil {
		// Claude may return non-zero exit but still produce JSON output.
		if len(out) == 0 {
			return RunResult{}, fmt.Errorf("claude CLI failed: %w", err)
		}
	}

	var cOut ClaudeJSONOutput
	if err := json.Unmarshal(out, &cOut); err != nil {
		return RunResult{}, fmt.Errorf("parse claude output: %w\nraw: %s", err, string(out[:min(len(out), 500)]))
	}

	totalIn := cOut.Usage.InputTokens + cOut.Usage.CacheCreation.Ephemeral1H + cOut.Usage.CacheRead

	return RunResult{
		Answer:     cOut.Result,
		Turns:      cOut.NumTurns,
		TokensIn:   totalIn,
		TokensOut:  cOut.Usage.OutputTokens,
		CostUSD:    cOut.CostUSD,
		DurationMS: cOut.DurationMS,
	}, nil
}
