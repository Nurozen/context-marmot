package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
				Content: "No LLM provider configured. Slash commands are available: /tag, /type, /verify, /delete, /link, /merge, /unlink, /untag",
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
	affectedIDs := collectAffectedNodeIDs(cmd, req.SelectedNodes)

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
		undoID := fmt.Sprintf("undo-%d", time.Now().UnixMilli())
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
func collectAffectedNodeIDs(cmd *curator.SlashCommand, selectedNodes []string) []string {
	seen := make(map[string]bool)
	var ids []string

	// Selected nodes (used by tag, untag, type, delete).
	for _, id := range selectedNodes {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	// Command args that might be node IDs (used by link, unlink, merge).
	switch cmd.Name {
	case "link", "unlink":
		// /link <source> <relation> <target>
		if len(cmd.Args) >= 3 {
			for _, idx := range []int{0, 2} {
				id := cmd.Args[idx]
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	case "merge":
		// /merge <A> <B>
		for _, arg := range cmd.Args {
			if !seen[arg] {
				seen[arg] = true
				ids = append(ids, arg)
			}
		}
	}

	return ids
}

// handleLLMChat builds a system prompt with graph context and calls the LLM
// for a natural language response.
func (s *Server) handleLLMChat(w http.ResponseWriter, _ *http.Request, req curator.ChatRequest) {
	// Build graph stats.
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
		outEdges := g.GetEdges(n.ID, graph.Outbound)
		totalEdges += len(outEdges)
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

	// Build selected node summaries.
	var selectedSummaries []curator.APINodeSummary
	for _, id := range req.SelectedNodes {
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

	systemPrompt := curator.BuildSystemPrompt(stats, selectedSummaries)

	// Build the LLM message history.
	messages := make([]llm.ChatMessage, 0, len(req.History)+1)
	for _, h := range req.History {
		if h.Role == "system" {
			continue
		}
		messages = append(messages, llm.ChatMessage{
			Role:    h.Role,
			Content: h.Content,
		})
	}
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: req.Message,
	})

	chatReq := llm.ChatRequest{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		MaxTokens:    1024,
	}

	ctx := context.Background()
	text, err := s.llmChat.Chat(ctx, chatReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LLM error: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, curator.ChatResponse{
		Message: curator.ChatMessage{
			Role:    "assistant",
			Content: text,
		},
	})
}
