package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/verify"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// context_query
// ---------------------------------------------------------------------------

// HandleContextQuery is the handler for the context_query MCP tool.
// It embeds the query, searches the embedding index for entry nodes,
// traverses the graph from those nodes, and returns compacted XML.
func (e *Engine) HandleContextQuery(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := req.GetString("query", "")
	if query == "" {
		return mcp.NewToolResultError("query parameter is required"), nil
	}

	depth := req.GetInt("depth", 2)
	if depth < 0 || depth > 10 {
		depth = 2
	}
	budget := req.GetInt("budget", 0)
	if budget <= 0 {
		budget = e.defaultTokenBudget()
	}
	if budget > 100000 {
		budget = 100000
	}
	mode := req.GetString("mode", "adjacency")
	includeSuperseded := req.GetBool("include_superseded", false)

	// Step 1: Embed the query.
	queryVec, err := e.Embedder.Embed(query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embed query: %v", err)), nil
	}

	// Step 2: Search embedding index for top-k entry nodes.
	topK := 5
	var results []embedding.ScoredResult
	if includeSuperseded {
		results, err = e.EmbeddingStore.Search(queryVec, topK, e.Embedder.Model())
	} else {
		results, err = e.EmbeddingStore.SearchActive(queryVec, topK, e.Embedder.Model())
	}
	if err != nil {
		// If model mismatch or empty store, return empty result gracefully.
		emptyXML := `<context_result tokens="0" nodes="0">` + "\n" + `</context_result>`
		return mcp.NewToolResultText(emptyXML), nil
	}

	entryIDs := make([]string, 0, len(results))
	for _, r := range results {
		entryIDs = append(entryIDs, r.NodeID)
	}

	if len(entryIDs) == 0 {
		emptyXML := `<context_result tokens="0" nodes="0">` + "\n" + `</context_result>`
		return mcp.NewToolResultText(emptyXML), nil
	}

	// Inject heat map weights for traversal priority.
	var heatWeights map[string]float64
	if e.HeatMap != nil {
		heatWeights = e.HeatMap.GetWeights(entryIDs)
	}

	// Step 3: Traverse graph from entry nodes.
	cfg := traversal.TraversalConfig{
		EntryIDs:          entryIDs,
		MaxDepth:          depth,
		TokenBudget:       budget,
		Mode:              mode,
		IncludeSuperseded: includeSuperseded,
		HeatWeights:       heatWeights,
	}
	subgraph := traversal.Traverse(e.Graph, cfg)

	// Step 4: Compact into XML.
	compacted := traversal.Compact(e.Graph, subgraph, budget)

	// Record co-access for heat map.
	if e.HeatMap != nil && len(subgraph.Nodes) >= 2 {
		resultIDs := make([]string, len(subgraph.Nodes))
		for i, n := range subgraph.Nodes {
			resultIDs[i] = n.ID
		}
		e.HeatMap.RecordCoAccess(resultIDs, heatmap.DefaultLearningRate)
	}

	return mcp.NewToolResultText(compacted.XML), nil
}

// ---------------------------------------------------------------------------
// context_write
// ---------------------------------------------------------------------------

// WriteEdgeInput is the JSON schema for an edge in context_write input.
type WriteEdgeInput struct {
	Target   string `json:"target"`
	Relation string `json:"relation"`
}

// WriteSourceInput is the JSON schema for a source reference in context_write input.
type WriteSourceInput struct {
	Path  string `json:"path,omitempty"`
	Lines []int  `json:"lines,omitempty"`
	Hash  string `json:"hash,omitempty"`
}

// WriteResult is the JSON response from context_write.
type WriteResult struct {
	NodeID string `json:"node_id"`
	Hash   string `json:"hash"`
	Status string `json:"status"`
}

// HandleContextWrite is the handler for the context_write MCP tool.
// It constructs a Node, validates structural acyclicity, persists via the
// node store, and updates the embedding index.
func (e *Engine) HandleContextWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	id := req.GetString("id", "")
	if id == "" {
		return mcp.NewToolResultError("id parameter is required"), nil
	}
	if err := node.ValidateNodeID(id); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid node ID: %v", err)), nil
	}
	nodeType := req.GetString("type", "concept")
	namespace := req.GetString("namespace", "default")

	mu := e.namespaceLock(namespace)
	mu.Lock()
	defer mu.Unlock()

	summary := req.GetString("summary", "")
	nodeCtx := req.GetString("context", "")

	// Parse edges.
	var edges []node.Edge
	if rawEdges, ok := args["edges"]; ok {
		edgeBytes, err := json.Marshal(rawEdges)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid edges: %v", err)), nil
		}
		var inputEdges []WriteEdgeInput
		if err := json.Unmarshal(edgeBytes, &inputEdges); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid edges: %v", err)), nil
		}
		for _, ie := range inputEdges {
			if ie.Target == "" {
				return mcp.NewToolResultError("edge target must not be empty"), nil
			}
			if ie.Relation == "" {
				return mcp.NewToolResultError("edge relation must not be empty"), nil
			}
			edges = append(edges, node.Edge{
				Target:   ie.Target,
				Relation: node.EdgeRelation(ie.Relation),
				Class:    node.ClassifyRelation(ie.Relation),
			})
		}
	}

	// Parse source.
	var source node.Source
	if rawSource, ok := args["source"]; ok {
		srcBytes, err := json.Marshal(rawSource)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid source: %v", err)), nil
		}
		var inputSource WriteSourceInput
		if err := json.Unmarshal(srcBytes, &inputSource); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid source: %v", err)), nil
		}
		source.Path = inputSource.Path
		source.Hash = inputSource.Hash
		if len(inputSource.Lines) >= 2 {
			source.Lines = [2]int{inputSource.Lines[0], inputSource.Lines[1]}
		}
	}

	// Construct node.
	n := &node.Node{
		ID:        id,
		Type:      nodeType,
		Namespace: namespace,
		Status:    node.StatusActive,
		Source:    source,
		Edges:     edges,
		Summary:   summary,
		Context:   nodeCtx,
	}

	// Validate cross-namespace edges against bridge manifests.
	if e.NSManager != nil {
		for _, edge := range edges {
			qid := e.NSManager.ParseQualifiedID(edge.Target, namespace)
			if qid.Namespace != namespace {
				if err := e.NSManager.ValidateCrossNamespaceEdge(namespace, qid.Namespace, string(edge.Relation)); err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("cross-namespace edge rejected: %v", err)), nil
				}
			}
		}
	}

	// Determine whether this is a create or update before any mutation.
	_, nodeExists := e.Graph.GetNode(id)
	isNew := !nodeExists

	// Set ValidFrom on first write (new node only).
	now := time.Now().UTC().Format(time.RFC3339)
	if isNew {
		n.ValidFrom = now
	}

	// Run CRUD classification if classifier is available.
	if e.Classifier != nil {
		classResult, classErr := e.Classifier.Classify(ctx, n, e.Graph)
		if classErr == nil {
			switch classResult.Action {
			case "NOOP":
				// Content is essentially identical to existing node — skip write.
				existing, ok := e.Graph.GetNode(classResult.TargetNodeID)
				if ok {
					result := WriteResult{
						NodeID: existing.ID,
						Hash:   verify.ComputeNodeHash(existing),
						Status: "noop",
					}
					return mcp.NewToolResultJSON(result)
				}
				// If target not found, fall through to ADD behavior.
			case "SUPERSEDE":
				// Soft-delete the target, then continue to write the new node.
				if classResult.TargetNodeID != "" && classResult.TargetNodeID != id {
					_ = e.NodeStore.SoftDeleteNode(classResult.TargetNodeID, id)
					if reloaded, loadErr := e.NodeStore.LoadNode(e.NodeStore.NodePath(classResult.TargetNodeID)); loadErr == nil {
						_ = e.Graph.UpsertNode(reloaded)
					}
					_ = e.EmbeddingStore.UpdateStatus(classResult.TargetNodeID, node.StatusSuperseded)
				}
			// ADD and UPDATE both proceed with the normal write path below.
			}
		}
		// On classifier error: fall through to normal write (safe degradation).
	}

	// Validate structural acyclicity using read-only cycle check.
	// This avoids mutating the graph during validation, preventing race
	// conditions with concurrent requests.
	for _, edge := range edges {
		if edge.Class == node.Structural {
			if e.Graph.WouldCreateCycle(id, edge.Target) {
				return mcp.NewToolResultError(fmt.Sprintf(
					"structural cycle detected: edge %s -> %s would create a cycle",
					id, edge.Target)), nil
			}
		}
	}

	// Upsert node into graph.
	if err := e.Graph.UpsertNode(n); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("graph upsert: %v", err)), nil
	}

	// Persist to disk via node store.
	if err := e.NodeStore.SaveNode(n); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("save node: %v", err)), nil
	}

	// Update embedding index.
	embedText := summary
	if nodeCtx != "" {
		ctxSnip := nodeCtx
		if len(ctxSnip) > 6000 {
			ctxSnip = ctxSnip[:6000]
		}
		embedText = summary + "\n\n" + ctxSnip
	}
	if embedText != "" {
		vec, err := e.Embedder.Embed(embedText)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embed summary: %v", err)), nil
		}
		summaryHash := sha256Hex(embedText)
		if err := e.EmbeddingStore.Upsert(id, vec, summaryHash, e.Embedder.Model()); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("upsert embedding: %v", err)), nil
		}
	}

	// Compute node content hash for the response.
	nodeHash := verify.ComputeNodeHash(n)

	writeStatus := "created"
	if !isNew {
		writeStatus = "updated"
	}

	// Notify summary scheduler of change (best-effort).
	if e.SummaryScheduler != nil {
		if metas, err := e.NodeStore.ListNodes(); err == nil {
			e.SummaryScheduler.NotifyChange(len(metas))
		}
	}

	// Reindex neighbors whose edges point to this node (background, non-blocking).
	e.reindexNeighbors(id)

	result := WriteResult{
		NodeID: id,
		Hash:   nodeHash,
		Status: writeStatus,
	}
	return mcp.NewToolResultJSON(result)
}

// ---------------------------------------------------------------------------
// context_verify
// ---------------------------------------------------------------------------

// VerifyIssue is a single issue in the verify response.
type VerifyIssue struct {
	NodeID    string `json:"node_id"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
}

// VerifyResult is the JSON response from context_verify.
type VerifyResult struct {
	Issues []VerifyIssue `json:"issues"`
	Total  int           `json:"total"`
}

// HandleContextVerify is the handler for the context_verify MCP tool.
// It runs integrity and/or staleness checks on requested nodes.
func (e *Engine) HandleContextVerify(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	check := req.GetString("check", "all")

	// Parse node_ids.
	var nodeIDs []string
	args := req.GetArguments()
	if rawIDs, ok := args["node_ids"]; ok {
		idsBytes, err := json.Marshal(rawIDs)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid node_ids: %v", err)), nil
		}
		if err := json.Unmarshal(idsBytes, &nodeIDs); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid node_ids: %v", err)), nil
		}
	}

	// Collect the nodes to verify.
	var nodes []*node.Node
	if len(nodeIDs) == 0 {
		// Verify all nodes in the graph.
		nodes = e.Graph.AllNodes()
	} else {
		for _, id := range nodeIDs {
			if n, ok := e.Graph.GetNode(id); ok {
				nodes = append(nodes, n)
			}
		}
	}

	var issues []VerifyIssue

	// Run integrity check (dangling edges, structural cycles).
	if check == "integrity" || check == "all" {
		integrityIssues := verify.VerifyIntegrity(nodes)
		for _, ii := range integrityIssues {
			issues = append(issues, VerifyIssue{
				NodeID:   ii.NodeID,
				Type:     string(ii.IssueType),
				Message:  ii.Message,
				Severity: string(ii.Severity),
			})
		}
	}

	// Run staleness check.
	if check == "staleness" || check == "all" {
		for _, n := range nodes {
			if n.Source.Path == "" {
				continue
			}
			status, err := verify.VerifyStaleness(n)
			if err != nil {
				issues = append(issues, VerifyIssue{
					NodeID:   n.ID,
					Type:     "staleness_error",
					Message:  err.Error(),
					Severity: "warning",
				})
				continue
			}
			if status.IsStale {
				issues = append(issues, VerifyIssue{
					NodeID:   n.ID,
					Type:     "stale",
					Message:  fmt.Sprintf("source hash mismatch: stored=%s current=%s", status.StoredHash, status.CurrentHash),
					Severity: "warning",
				})
			}
		}
	}

	result := VerifyResult{
		Issues: issues,
		Total:  len(issues),
	}
	if result.Issues == nil {
		result.Issues = []VerifyIssue{}
	}

	return mcp.NewToolResultJSON(result)
}

// ---------------------------------------------------------------------------
// context_delete
// ---------------------------------------------------------------------------

// DeleteResult is the JSON response from context_delete.
type DeleteResult struct {
	NodeID       string `json:"node_id"`
	Status       string `json:"status"`
	SupersededBy string `json:"superseded_by,omitempty"`
}

// HandleContextDelete marks a node as superseded/soft-deleted.
func (e *Engine) HandleContextDelete(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := req.GetString("id", "")
	if id == "" {
		return mcp.NewToolResultError("id parameter is required"), nil
	}
	supersededBy := req.GetString("superseded_by", "")

	// Verify node exists.
	existing, ok := e.Graph.GetNode(id)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("node %q not found", id)), nil
	}

	mu := e.namespaceLock(existing.Namespace)
	mu.Lock()
	defer mu.Unlock()

	// Re-fetch inside the lock so concurrent deletes see the updated status.
	current, ok := e.Graph.GetNode(id)
	if !ok || current.Status == node.StatusSuperseded {
		return mcp.NewToolResultError(fmt.Sprintf("node %q not found", id)), nil
	}

	// Soft-delete on disk.
	if err := e.NodeStore.SoftDeleteNode(id, supersededBy); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("soft delete: %v", err)), nil
	}

	// Reload node from disk and upsert into graph to update in-memory status.
	path := e.NodeStore.NodePath(id)
	updated, err := e.NodeStore.LoadNode(path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("reload node: %v", err)), nil
	}
	if err := e.Graph.UpsertNode(updated); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("graph upsert: %v", err)), nil
	}

	// Update embedding status.
	_ = e.EmbeddingStore.UpdateStatus(id, node.StatusSuperseded)

	// Notify summary scheduler of change (best-effort).
	if e.SummaryScheduler != nil {
		if metas, err := e.NodeStore.ListNodes(); err == nil {
			e.SummaryScheduler.NotifyChange(len(metas))
		}
	}

	// Reindex neighbors whose edges pointed to the deleted node (background).
	e.reindexNeighbors(id)

	result := DeleteResult{
		NodeID:       id,
		Status:       node.StatusSuperseded,
		SupersededBy: supersededBy,
	}
	return mcp.NewToolResultJSON(result)
}

// sha256Hex returns the hex-encoded SHA-256 hash of s.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
