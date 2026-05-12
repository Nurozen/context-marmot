package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nurozen/context-marmot/internal/codemode"
	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/llm"
)

// handleChat handles POST /api/chat. It routes slash commands directly to
// ParseCommand + ExecuteCommand, and natural language messages to the LLM
// chat provider (if configured).
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req curator.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	// Slash command routing: if message starts with '/', dispatch directly.
	if strings.HasPrefix(strings.TrimSpace(req.Message), "/") {
		s.handleSlashCommand(w, req)
		return
	}

	// Natural language: requires an LLM provider.
	if s.llmChat == nil {
		writeJSON(w, http.StatusOK, curator.ChatResponse{
			Message: curator.ChatMessage{
				Role:    "assistant",
				Content: "No LLM provider configured. Run `marmot configure` and pick OpenAI or Anthropic as the classifier provider to enable NL chat. Slash commands are still available: /tag, /type, /verify, /delete, /link, /merge, /unlink, /untag",
			},
		})
		return
	}

	s.handleLLMChat(w, r, req)
}

// handleSlashCommand parses and executes a slash command, returning the result
// as a ChatResponse.
func (s *Server) handleSlashCommand(w http.ResponseWriter, req curator.ChatRequest) {
	cmd, isSlash := curator.ParseCommand(req.Message)
	if !isSlash || cmd == nil {
		writeJSON(w, http.StatusOK, curator.ChatResponse{
			Message: curator.ChatMessage{
				Role:    "assistant",
				Content: "Could not parse command. Type / to see available commands.",
			},
		})
		return
	}

	// Collect potentially affected node IDs for undo snapshot.
	affectedIDs := s.collectAffectedNodeIDs(cmd, req.SelectedNodes)
	unlock := s.lockNamespacesForIDs(affectedIDs, req.Namespace)
	defer unlock()

	// Snapshot nodes before mutation.
	snapshots := curator.SnapshotNodes(s.engine.NodeStore, req.Namespace, affectedIDs)

	ctx := context.Background()
	result, err := curator.ExecuteCommand(ctx, cmd, s.engine, req.SelectedNodes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "command execution error: "+err.Error())
		return
	}

	resp := curator.ChatResponse{
		Message: curator.ChatMessage{
			Role:    "assistant",
			Content: result.Message,
		},
	}

	// If the command mutated nodes, push undo entry and notify SSE clients.
	if result.Success && len(result.MutatedNodes) > 0 {
		undoID := fmt.Sprintf("undo-%d", time.Now().UnixNano())
		entry := curator.UndoEntry{
			ID:        undoID,
			SessionID: req.SessionID,
			Timestamp: time.Now(),
			Snapshots: snapshots,
		}
		s.undoStack.Push(req.SessionID, entry)
		resp.UndoID = undoID
		s.NotifyChange()
	}

	writeJSON(w, http.StatusOK, resp)
}

// collectAffectedNodeIDs gathers all node IDs that might be mutated by a command.
// This includes selected nodes plus any node IDs referenced in command args.
func (s *Server) collectAffectedNodeIDs(cmd *curator.SlashCommand, selectedNodes []string) []string {
	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		if n, ok := s.engine.ResolveNodeID(id); ok {
			id = n.ID
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	// Selected nodes (used by tag, untag, type, delete).
	for _, id := range selectedNodes {
		add(id)
	}

	// Command args that might be node IDs (used by link, unlink, merge).
	switch cmd.Name {
	case "link", "unlink":
		// /link <source> <relation> <target>
		if len(cmd.Args) >= 3 {
			for _, idx := range []int{0, 2} {
				add(cmd.Args[idx])
			}
		}
	case "merge":
		// /merge <A> <B>
		for _, arg := range cmd.Args {
			add(arg)
		}
		if len(cmd.Args) >= 2 {
			fromID := cmd.Args[1]
			if n, ok := s.engine.ResolveNodeID(fromID); ok {
				fromID = n.ID
			}
			for _, e := range s.engine.GetGraph().GetEdges(fromID, graph.Inbound) {
				add(e.Target)
			}
		}
	}

	return ids
}

func (s *Server) lockNamespacesForIDs(ids []string, fallbackNamespace string) func() {
	namespaces := make(map[string]struct{})
	for _, id := range ids {
		ns := fallbackNamespace
		if n, ok := s.engine.ResolveNodeID(id); ok {
			ns = n.Namespace
		}
		if ns == "" {
			ns = "default"
		}
		namespaces[ns] = struct{}{}
	}
	ordered := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		ordered = append(ordered, ns)
	}
	sort.Strings(ordered)
	for _, ns := range ordered {
		s.engine.NamespaceLock(ns).Lock()
	}
	return func() {
		for i := len(ordered) - 1; i >= 0; i-- {
			s.engine.NamespaceLock(ordered[i]).Unlock()
		}
	}
}

// PerPhaseChatTimeout caps how long a single LLM call may take. The
// underlying HTTP client also enforces its own 120s timeout; this is the
// shorter, per-phase guarantee so a slow phase-1 call cannot starve phase 2.
const PerPhaseChatTimeout = 90 * time.Second

// handleLLMChat runs the two-phase code-mode chat flow:
//
//  1. Build a phase-1 system prompt that documents the `client` JS API and
//     ask the LLM to either answer directly or emit a single code block.
//  2. If the response contains a code block, execute it in a goja sandbox.
//     Then call the LLM again with a phase-2 prompt that includes the code
//     and the execution result, asking for a natural-language answer.
//
// If phase 1 returns no code block, we treat the response as the final
// answer and skip phase 2. Each LLM call is capped at PerPhaseChatTimeout
// so a stuck request can't hang the user indefinitely. Phase boundaries
// are logged to stderr so operators can see where time goes.
func (s *Server) handleLLMChat(w http.ResponseWriter, r *http.Request, req curator.ChatRequest) {
	// Build graph stats + selected-node summaries (used by phase-1 prompt).
	stats, selectedSummaries := s.buildChatContext(req.SelectedNodes)

	// Phase-1 prompt: documents the client API + graph context.
	// TODO: read engine.ReadOnly once feat/package-docs lands on main.
	phase1Prompt := codemode.BuildPhase1Prompt(stats, selectedSummaries, false)

	// Build the LLM message history. Strip past assistant code blocks so we
	// don't re-train the model on its previous code on phase-1 turns — keep
	// only the assistant's final natural-language text.
	history := make([]llm.ChatMessage, 0, len(req.History)+1)
	for _, h := range req.History {
		if h.Role == "system" {
			continue
		}
		content := h.Content
		if h.Role == "assistant" {
			content = stripCodeBlocks(content)
		}
		history = append(history, llm.ChatMessage{Role: h.Role, Content: content})
	}
	userMsg := llm.ChatMessage{Role: "user", Content: req.Message}

	// Use the request context so client disconnects abort downstream work.
	ctx := r.Context()
	turnStart := time.Now()
	fmt.Fprintf(os.Stderr, "chat[%s]: turn start, model=%s, history=%d msgs, msg=%q\n",
		req.SessionID, s.llmChat.Model(), len(history), truncatePreview(req.Message, 80))

	// Phase 1.
	phase1Ctx, phase1Cancel := context.WithTimeout(ctx, PerPhaseChatTimeout)
	phase1Start := time.Now()
	phase1, err := s.llmChat.Chat(phase1Ctx, llm.ChatRequest{
		SystemPrompt: phase1Prompt,
		Messages:     append(history, userMsg),
		MaxTokens:    4096,
	})
	phase1Cancel()
	fmt.Fprintf(os.Stderr, "chat[%s]: phase 1 done in %s (err=%v, bytes=%d)\n",
		req.SessionID, time.Since(phase1Start).Round(time.Millisecond), err, len(phase1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LLM error (phase 1): "+err.Error())
		return
	}

	// Try to extract code. If none → phase-1 message IS the final answer.
	code := codemode.ExtractCode(phase1)
	if code == "" {
		fmt.Fprintf(os.Stderr, "chat[%s]: no code block in phase 1 — returning direct answer (total %s)\n",
			req.SessionID, time.Since(turnStart).Round(time.Millisecond))
		writeJSON(w, http.StatusOK, curator.ChatResponse{
			Message: curator.ChatMessage{Role: "assistant", Content: phase1},
		})
		return
	}

	// Execute the code in a fresh sandbox. Writes are enabled by default —
	// the LLM is told to only call mutation methods when the user asked for
	// a change. Each successful write records a MutationRecord that surfaces
	// in the chat response and an undo entry so the user can roll back.
	writeCtx := &codemode.WriteContext{
		SessionID:     req.SessionID,
		SelectedNodes: req.SelectedNodes,
		Namespace:     req.Namespace,
		UndoStack:     s.undoStack,
		NotifyChange:  s.NotifyChange,
	}
	execStart := time.Now()
	execResult := s.codeExecutor.ExecuteWithWrites(ctx, code, writeCtx)
	fmt.Fprintf(os.Stderr, "chat[%s]: sandbox exec done in %s (err=%q)\n",
		req.SessionID, time.Since(execStart).Round(time.Millisecond), execResult.Error)

	// Recovery: if the first attempt threw a runtime error (typically
	// "node not found" or a typo'd method name), give the model one more
	// chance with the error fed back as context. Capped at 1 retry so the
	// worst case is 3 LLM calls per turn (phase 1 → retry → phase 2).
	if execResult.Error != "" && len(execResult.Mutations) == 0 {
		retryPrompt := phase1Prompt +
			"\n\n## Your previous attempt failed\n" +
			"You ran this code:\n```js\n" + code + "\n```\n" +
			"It threw:\n```\n" + execResult.Error + "\n```\n" +
			"Try a different approach. If the error suggests a node-id mismatch, " +
			"call `client.search(...)` first to discover the correct ID, then drill " +
			"in with `client.getNode(...)` or `client.getNeighbors(...)`. " +
			"Emit a fresh `js` code block.\n"

		retryCtx, retryCancel := context.WithTimeout(ctx, PerPhaseChatTimeout)
		retryStart := time.Now()
		retryResp, retryErr := s.llmChat.Chat(retryCtx, llm.ChatRequest{
			SystemPrompt: retryPrompt,
			Messages:     append(history, userMsg),
			MaxTokens:    4096,
		})
		retryCancel()
		fmt.Fprintf(os.Stderr, "chat[%s]: retry done in %s (err=%v)\n",
			req.SessionID, time.Since(retryStart).Round(time.Millisecond), retryErr)
		if retryErr == nil {
			if retryCode := codemode.ExtractCode(retryResp); retryCode != "" {
				retryResult := s.codeExecutor.ExecuteWithWrites(ctx, retryCode, writeCtx)
				// Only accept the retry result if it didn't also fail.
				// (A successful empty result still beats a thrown error.)
				if retryResult.Error == "" {
					code = retryCode
					execResult = retryResult
				}
			}
		}
	}

	codeRun := &curator.CodeRunInfo{
		Code:       code,
		Result:     execResult.Value,
		Logs:       execResult.Logs,
		Error:      execResult.Error,
		DurationMS: execResult.DurationMS,
		Truncated:  execResult.Truncated,
		Mutations:  execResult.Mutations,
	}

	// Phase 2: synthesize a natural-language answer from the execution.
	phase2Prompt := codemode.BuildPhase2Prompt(req.Message, code, execResult)
	phase2Ctx, phase2Cancel := context.WithTimeout(ctx, PerPhaseChatTimeout)
	phase2Start := time.Now()
	phase2, err := s.llmChat.Chat(phase2Ctx, llm.ChatRequest{
		SystemPrompt: phase2Prompt,
		// Phase 2 only needs the user's original question — the prompt itself
		// already contains the code and result.
		Messages:  []llm.ChatMessage{userMsg},
		MaxTokens: 4096,
	})
	phase2Cancel()
	fmt.Fprintf(os.Stderr, "chat[%s]: phase 2 done in %s (err=%v, total %s)\n",
		req.SessionID, time.Since(phase2Start).Round(time.Millisecond), err,
		time.Since(turnStart).Round(time.Millisecond))
	if err != nil {
		// Best effort: return phase-1 message + the code-run info so the user
		// still sees something concrete.
		writeJSON(w, http.StatusOK, curator.ChatResponse{
			Message: curator.ChatMessage{
				Role:    "assistant",
				Content: phase1 + "\n\n_(failed to summarize the result: " + err.Error() + ")_",
			},
			CodeRun: codeRun,
		})
		return
	}

	writeJSON(w, http.StatusOK, curator.ChatResponse{
		Message: curator.ChatMessage{Role: "assistant", Content: phase2},
		CodeRun: codeRun,
	})
}

// buildChatContext assembles GraphStats and the selected-node summaries
// for the phase-1 system prompt. Extracted so tests can call it.
func (s *Server) buildChatContext(selectedNodeIDs []string) (curator.GraphStats, []curator.APINodeSummary) {
	g := s.engine.GetGraph()
	allNodes := g.AllActiveNodes()

	nsSet := make(map[string]bool)
	totalEdges := 0
	for _, n := range allNodes {
		ns := n.Namespace
		if ns == "" {
			ns = "default"
		}
		nsSet[ns] = true
		totalEdges += len(g.GetEdges(n.ID, graph.Outbound))
	}
	namespaces := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		namespaces = append(namespaces, ns)
	}

	stats := curator.GraphStats{
		NodeCount:  len(allNodes),
		EdgeCount:  totalEdges,
		Namespaces: namespaces,
	}

	var selectedSummaries []curator.APINodeSummary
	for _, id := range selectedNodeIDs {
		n, ok := s.engine.ResolveNodeID(id)
		if !ok {
			continue
		}
		outEdges := g.GetEdges(n.ID, graph.Outbound)
		inEdges := g.GetEdges(n.ID, graph.Inbound)
		tags := n.Tags
		if tags == nil {
			tags = []string{}
		}
		selectedSummaries = append(selectedSummaries, curator.APINodeSummary{
			ID:      n.ID,
			Type:    n.Type,
			Summary: n.Summary,
			Tags:    tags,
			Edges:   len(outEdges) + len(inEdges),
		})
	}
	return stats, selectedSummaries
}

// truncatePreview returns at most n bytes of s, suffixed with "..." when
// truncated. Used purely for stderr logging.
func truncatePreview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripCodeBlocks removes fenced code blocks from a string. Used to clean
// past assistant turns before re-sending them on phase-1 prompts so the
// model doesn't try to "continue" stale code.
func stripCodeBlocks(s string) string {
	const fence = "```"
	out := s
	for {
		i := strings.Index(out, fence)
		if i < 0 {
			break
		}
		j := strings.Index(out[i+len(fence):], fence)
		if j < 0 {
			break
		}
		end := i + len(fence) + j + len(fence)
		out = out[:i] + out[end:]
	}
	return strings.TrimSpace(out)
}
