package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/verify"
	"github.com/nurozen/context-marmot/internal/warren"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// context_query
// ---------------------------------------------------------------------------

// HandleContextQuery is the handler for the context_query MCP tool.
// It embeds the query, searches the embedding index for entry nodes,
// traverses the graph from those nodes, and returns compacted XML.
func (e *Engine) HandleContextQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	queryVec, err := embedWithContext(ctx, e.Embedder, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embed query: %v", err)), nil
	}
	if err := ctx.Err(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query cancelled: %v", err)), nil
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

	// Step 2b: If cross-vault bridges exist, also search remote vault embeddings.
	if e.VaultRegistry != nil {
		for _, vid := range e.VaultRegistry.KnownVaultIDs() {
			if vid == "" || vid == e.LocalVaultID {
				continue
			}
			remoteStore, err := e.VaultRegistry.ResolveEmbeddingStore(vid)
			if err != nil {
				// Best-effort: local results still return, but the degraded
				// vault is visible (once per vault) instead of vanishing.
				e.warnVaultOnce(vid, "warren vault %q embedding store unavailable, excluded from context_query: %v", vid, err)
				continue
			}
			var remoteResults []embedding.ScoredResult
			var searchErr error
			if includeSuperseded {
				remoteResults, searchErr = remoteStore.Search(queryVec, 3, e.Embedder.Model())
			} else {
				remoteResults, searchErr = remoteStore.SearchActive(queryVec, 3, e.Embedder.Model())
			}
			if searchErr != nil {
				e.warnVaultOnce(vid, "warren vault %q search failed, excluded from context_query: %v", vid, searchErr)
			}
			// Prefix remote results with @vault-id/ so BridgedGraphResolver can resolve them.
			for _, r := range remoteResults {
				results = append(results, embedding.ScoredResult{
					NodeID: "@" + vid + "/" + r.NodeID,
					Score:  r.Score,
				})
			}
		}
	}
	results = dedupeAndRankResults(results, 10)

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
	resolver := e.graphResolver()
	subgraph := traversal.Traverse(resolver, cfg)

	// Step 4: Compact into XML.
	compacted := traversal.Compact(resolver, subgraph, budget)

	// Record co-access for heat map (local nodes only — cross-vault
	// @-prefixed IDs are excluded because GetWeights only receives local
	// entry IDs from embedding search, so remote pairs would be orphaned).
	if e.HeatMap != nil && len(subgraph.Nodes) >= 2 {
		var resultIDs []string
		for _, n := range subgraph.Nodes {
			if !strings.HasPrefix(n.ID, "@") {
				resultIDs = append(resultIDs, n.ID)
			}
		}
		if len(resultIDs) >= 2 {
			e.HeatMap.RecordCoAccess(resultIDs, heatmap.DefaultLearningRate)
			// Persist heat data to disk so it survives restarts.
			_ = heatmap.Save(e.MarmotDir, e.HeatMap)
		}
	}

	return mcp.NewToolResultText(compacted.XML), nil
}

func dedupeAndRankResults(results []embedding.ScoredResult, limit int) []embedding.ScoredResult {
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	seen := make(map[string]bool, len(results))
	out := make([]embedding.ScoredResult, 0, min(limit, len(results)))
	for _, r := range results {
		if seen[r.NodeID] {
			continue
		}
		seen[r.NodeID] = true
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

type contextEmbedder interface {
	EmbedContext(context.Context, string) ([]float32, error)
}

func embedWithContext(ctx context.Context, embedder interface {
	Embed(string) ([]float32, error)
}, text string) ([]float32, error) {
	if ce, ok := embedder.(contextEmbedder); ok {
		return ce.EmbedContext(ctx, text)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vec, err := embedder.Embed(text)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return vec, nil
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
	// Provenance notes where a warren @-write landed (editable mount writes
	// go to the project's own checkout, never a materialized cache).
	Provenance string `json:"provenance,omitempty"`
	// Warning surfaces a non-fatal degradation (e.g. the node write landed
	// but its embedding upsert failed) instead of swallowing it.
	Warning string `json:"warning,omitempty"`
}

// knownWriteArgs is the set of top-level arguments accepted by context_write,
// mirroring the tool schema registered in server.go.
var knownWriteArgs = map[string]bool{
	"id":        true,
	"type":      true,
	"namespace": true,
	"summary":   true,
	"context":   true,
	"tags":      true,
	"edges":     true,
	"source":    true,
}

// writeArgAliases maps commonly misused argument names to the schema field
// the caller almost certainly meant, so validation errors can point clients
// (and their typos) at the right field.
var writeArgAliases = map[string]string{
	"content":     "context",
	"body":        "context",
	"text":        "context",
	"description": "summary",
	"title":       "summary",
	"node_id":     "id",
	"tag":         "tags",
	"edge":        "edges",
}

// validateWriteArgNames rejects unknown top-level context_write arguments.
// Historically unknown arguments were silently ignored, which turned client
// typos (e.g. "content" instead of "context") into empty-body nodes. Keys
// with a leading underscore are tolerated as protocol/client metadata.
// Returns an empty string when the arguments are valid.
func validateWriteArgNames(args map[string]any) string {
	var unknown []string
	for k := range args {
		if knownWriteArgs[k] || strings.HasPrefix(k, "_") {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	parts := make([]string, 0, len(unknown))
	for _, k := range unknown {
		if want, ok := writeArgAliases[k]; ok {
			parts = append(parts, fmt.Sprintf("%q (did you mean %q?)", k, want))
		} else {
			parts = append(parts, fmt.Sprintf("%q", k))
		}
	}
	return fmt.Sprintf("unknown argument(s) %s — valid arguments are: id, type, namespace, summary, context, tags, edges, source",
		strings.Join(parts, ", "))
}

// HandleContextWrite is the handler for the context_write MCP tool.
// It constructs a Node, validates structural acyclicity, persists via the
// node store, and updates the embedding index.
func (e *Engine) HandleContextWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	if msg := validateWriteArgNames(args); msg != "" {
		return mcp.NewToolResultError(msg), nil
	}

	id := req.GetString("id", "")
	if id == "" {
		return mcp.NewToolResultError("id parameter is required"), nil
	}
	if strings.HasPrefix(id, "@") {
		// Qualified @vault-id/... writes are accepted for active editable
		// warren mounts, exactly like the HTTP API/UI path (both go through
		// warren.WriteEditableNode so they cannot diverge).
		return e.handleWarrenContextWrite(req, id)
	}
	if err := node.ValidateNodeID(id); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid node ID: %v", err)), nil
	}
	nodeType := req.GetString("type", "concept")
	namespace := req.GetString("namespace", "default")

	// Auto-prefix namespace to ID when using namespaces, so the on-disk path
	// matches the ID. LLMs often omit the namespace prefix from the ID since
	// they pass it as a separate parameter.
	if namespace != "default" && !strings.HasPrefix(id, namespace+"/") {
		id = namespace + "/" + id
	}

	mu := e.NamespaceLock(namespace)
	mu.Lock()
	defer mu.Unlock()

	summary := req.GetString("summary", "")
	nodeCtx := req.GetString("context", "")
	if strings.TrimSpace(summary) == "" && strings.TrimSpace(nodeCtx) == "" {
		return mcp.NewToolResultError(`summary or context is required: put a searchable 1-2 sentence description in "summary" and the full node body in "context" — refusing to create an empty node`), nil
	}

	// Parse tags.
	var tags []string
	if rawTags, ok := args["tags"]; ok {
		tagBytes, err := json.Marshal(rawTags)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid tags: %v", err)), nil
		}
		var rawTagSlice []string
		if err := json.Unmarshal(tagBytes, &rawTagSlice); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid tags: %v", err)), nil
		}
		// Filter out empty/whitespace-only tags.
		for _, t := range rawTagSlice {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

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
			target := ie.Target
			// Auto-prefix namespace on edge targets for same-namespace references.
			// Skip cross-vault (@vault/...), already-prefixed targets, and targets
			// that start with another known namespace (cross-namespace edges).
			if namespace != "default" && !strings.HasPrefix(target, "@") && !strings.HasPrefix(target, namespace+"/") {
				shouldPrefix := true
				if idx := strings.Index(target, "/"); idx > 0 {
					if e.HasNamespace(target[:idx]) {
						shouldPrefix = false
					}
				}
				if shouldPrefix {
					target = namespace + "/" + target
				}
			}
			edges = append(edges, node.Edge{
				Target:   target,
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

		// Convert absolute source.path to relative (relative to project root).
		if source.Path != "" && filepath.IsAbs(source.Path) {
			projectRoot := e.EffectiveProjectRoot()
			if rel, err := filepath.Rel(projectRoot, source.Path); err == nil && !strings.HasPrefix(rel, "..") {
				source.Path = rel
			}
		}

		// Compute the source hash when omitted so staleness detection has a
		// baseline. Leave it empty if the file can't be read — the integrity
		// check reports missing sources separately.
		if source.Path != "" && source.Hash == "" {
			resolved := verify.ResolveSourcePath(source.Path, e.EffectiveProjectRoot())
			if h, err := verify.ComputeSourceHash(resolved, source.Lines); err == nil {
				source.Hash = h
			}
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
		Tags:      tags,
		Summary:   summary,
		Context:   nodeCtx,
	}

	// Validate cross-namespace edges against bridge manifests.
	// Skip edges that target a different vault (@vault-id/...) — those are
	// validated separately in the cross-vault check below.
	if err := e.validateCrossNamespaceEdges(edges, namespace); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("cross-namespace edge rejected: %v", err)), nil
	}

	// Validate cross-vault edges against bridge manifests.
	if err := e.validateCrossVaultEdges(edges, namespace); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("cross-vault edge rejected: %v", err)), nil
	}

	// Determine whether this is a create or update before any mutation.
	existingNode, nodeExists := e.GetGraph().GetNode(id)
	isNew := !nodeExists

	// For updates: if no new tags provided, keep existing tags.
	if !isNew && len(tags) == 0 && len(existingNode.Tags) > 0 {
		n.Tags = existingNode.Tags
	}

	// Set ValidFrom on first write (new node only).
	now := time.Now().UTC().Format(time.RFC3339)
	if isNew {
		n.ValidFrom = now
	}

	// Run CRUD classification if classifier is available.
	if e.Classifier != nil {
		classResult, classErr := e.Classifier.Classify(ctx, n, e.GetGraph())
		if classErr == nil {
			switch classResult.Action {
			case "NOOP":
				// Content is essentially identical to existing node — skip write.
				existing, ok := e.GetGraph().GetNode(classResult.TargetNodeID)
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
						_ = e.GetGraph().UpsertNode(reloaded)
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
			if e.GetGraph().WouldCreateCycle(id, edge.Target) {
				return mcp.NewToolResultError(fmt.Sprintf(
					"structural cycle detected: edge %s -> %s would create a cycle",
					id, edge.Target)), nil
			}
		}
	}

	if err := e.ensureNamespaceManifest(namespace); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("ensure namespace: %v", err)), nil
	}

	// Upsert node into graph.
	if err := e.GetGraph().UpsertNode(n); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("graph upsert: %v", err)), nil
	}

	// Persist to disk via node store.
	if err := e.NodeStore.SaveNode(n); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("save node: %v", err)), nil
	}

	// Update embedding index.
	tagStr := strings.Join(n.Tags, " ")
	embedText := summary
	if tagStr != "" {
		embedText = summary + " " + tagStr
	}
	if nodeCtx != "" {
		ctxSnip := nodeCtx
		if len(ctxSnip) > 6000 {
			ctxSnip = ctxSnip[:6000]
		}
		embedText = embedText + "\n\n" + ctxSnip
	}
	if embedText != "" {
		vec, err := embedWithContext(ctx, e.Embedder, embedText)
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

// handleWarrenContextWrite services context_write for qualified
// "@vault-id/node-id" IDs: updates to existing nodes of active *editable*
// warren mounts. It mirrors the HTTP API's warren node-update semantics
// (summary/context/tags updates of an existing node) and shares
// warren.WriteEditableNode, so the two paths produce byte-identical files.
// Creating brand-new nodes in a mounted project is not supported — do that
// in the project's own workspace.
func (e *Engine) handleWarrenContextWrite(req mcp.CallToolRequest, qualifiedID string) (*mcp.CallToolResult, error) {
	vaultID, localID, ok := warren.SplitQualifiedVaultID(qualifiedID)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("invalid qualified node ID %q: expected @vault-id/node-id", qualifiedID)), nil
	}
	// Self-alias guard (defense in depth over ActiveMounts forcing
	// Editable=false on alias statuses): an @-write to the workspace's own
	// vault ID would land in the warren checkout copy and split-brain the
	// live vault — including legacy state that still records the self
	// project as editable.
	if vaultID != "" && vaultID == e.LocalVaultID {
		return mcp.NewToolResultError(fmt.Sprintf("vault %q is this workspace's own vault; write the node locally as %q (no @ prefix) — self-alias warren mounts are read-through views of the live vault", vaultID, localID)), nil
	}
	mounts, err := warren.ActiveMounts(e.MarmotDir)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("warren mounts unavailable: %v", err)), nil
	}
	var mount warren.ProjectStatus
	found := false
	for _, m := range mounts {
		if m.VaultID == vaultID {
			mount = m
			found = true
			break
		}
	}
	if !found || !mount.Editable {
		return mcp.NewToolResultError(fmt.Sprintf("vault %q is not an editable warren mount in this workspace; run 'marmot warren edit --warren <id> <project>'", vaultID)), nil
	}
	if err := node.ValidateNodeID(localID); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid node ID: %v", err)), nil
	}

	summary := req.GetString("summary", "")
	nodeCtx := req.GetString("context", "")
	if strings.TrimSpace(summary) == "" && strings.TrimSpace(nodeCtx) == "" {
		return mcp.NewToolResultError(`summary or context is required: put a searchable 1-2 sentence description in "summary" and the full node body in "context" — refusing to blank a mounted node`), nil
	}
	args := req.GetArguments()
	var tags []string
	tagsProvided := false
	if rawTags, ok := args["tags"]; ok {
		tagsProvided = true
		tagBytes, err := json.Marshal(rawTags)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid tags: %v", err)), nil
		}
		if err := json.Unmarshal(tagBytes, &tags); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid tags: %v", err)), nil
		}
	}

	// Serialize concurrent MCP writes to the same mount.
	mu := e.NamespaceLock("@" + vaultID)
	mu.Lock()
	defer mu.Unlock()

	store := node.NewStore(mount.Path)
	diskNode, err := store.LoadNode(store.NodePath(localID))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("node %q not found in warren mount %s/%s — @-writes update existing mounted nodes; create new nodes in the project's own workspace", qualifiedID, mount.WarrenID, mount.ProjectID)), nil
	}
	embeddingChanged := false
	if summary != "" {
		diskNode.Summary = summary
		embeddingChanged = true
	}
	if nodeCtx != "" {
		diskNode.Context = nodeCtx
		embeddingChanged = true
	}
	if tagsProvided {
		diskNode.Tags = tags
	}

	var vec []float32
	var summaryHash, model string
	var warning string
	if embeddingChanged && e.Embedder != nil {
		if embedText := warren.EmbedText(diskNode); embedText != "" {
			v, embErr := e.Embedder.Embed(embedText)
			if embErr != nil {
				warning = "embedding not updated: " + embErr.Error()
			} else {
				vec = v
				summaryHash = sha256Hex(embedText)
				model = e.Embedder.Model()
			}
		}
	}
	writeWarning, err := warren.WriteEditableNode(mount, diskNode, vec, summaryHash, model)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("save warren node: %v", err)), nil
	}
	if warning == "" {
		warning = writeWarning
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "warning: warren editable write %s: %s\n", qualifiedID, warning)
	}
	// Make the write visible to already-cached cross-vault searches.
	if e.VaultRegistry != nil {
		if refreshErr := e.VaultRegistry.Refresh(vaultID); refreshErr != nil && !errors.Is(refreshErr, namespace.ErrNotLoaded) {
			fmt.Fprintf(os.Stderr, "warning: refresh after editable write failed for vault %q: %v\n", vaultID, refreshErr)
		}
	}
	result := WriteResult{
		NodeID:     qualifiedID,
		Hash:       verify.ComputeNodeHash(diskNode),
		Status:     "updated",
		Provenance: fmt.Sprintf("warren_mount %s/%s (editable): wrote to the project checkout at %s", mount.WarrenID, mount.ProjectID, mount.Path),
		Warning:    warning,
	}
	return mcp.NewToolResultJSON(result)
}

func (e *Engine) ensureNamespaceManifest(name string) error {
	if name == "" || name == "default" {
		return nil
	}
	ns, _, err := namespace.EnsureNamespace(e.MarmotDir, name, "")
	if err != nil {
		return err
	}
	e.nsMgrMu.Lock()
	defer e.nsMgrMu.Unlock()
	if e.NSManager != nil {
		e.NSManager.Namespaces[name] = ns
		return nil
	}
	mgr, mgrErr := namespace.NewManager(e.MarmotDir)
	if mgrErr != nil {
		return mgrErr
	}
	e.NSManager = mgr
	return nil
}

// ---------------------------------------------------------------------------
// context_verify
// ---------------------------------------------------------------------------

// VerifyIssue is a single issue in the verify response.
type VerifyIssue struct {
	NodeID   string `json:"node_id"`
	Type     string `json:"type"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
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
		nodes = e.GetGraph().AllNodes()
	} else {
		for _, id := range nodeIDs {
			if n, ok := e.ResolveNodeID(id); ok {
				nodes = append(nodes, n)
			}
		}
	}

	var issues []VerifyIssue
	projectRoot := e.EffectiveProjectRoot()

	// Run integrity check (dangling edges, structural cycles).
	if check == "integrity" || check == "all" {
		// When verifying a subset, resolve edge targets against the full
		// graph so edges to nodes outside the subset aren't flagged dangling.
		var knownIDs map[string]bool
		if len(nodeIDs) > 0 {
			all := e.GetGraph().AllNodes()
			knownIDs = make(map[string]bool, len(all))
			for _, n := range all {
				knownIDs[n.ID] = true
			}
		}
		integrityIssues := verify.VerifyIntegrityScoped(nodes, knownIDs, projectRoot)
		for _, ii := range integrityIssues {
			issues = append(issues, VerifyIssue{
				NodeID:   ii.NodeID,
				Type:     string(ii.IssueType),
				Message:  ii.Message,
				Severity: string(ii.Severity),
			})
		}
	}

	// Run staleness check. Nodes without a stored hash have no baseline to
	// compare against and are skipped (matching CLI verify behavior).
	if check == "staleness" || check == "all" {
		for _, n := range nodes {
			if n.Source.Path == "" || n.Source.Hash == "" {
				continue
			}
			status, err := verify.VerifyStaleness(n, projectRoot)
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

	// Try to find node, auto-prefixing namespace if the bare ID isn't found.
	existing, ok := e.ResolveNodeID(id)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("node %q not found", id)), nil
	}
	// Use the resolved (possibly prefixed) ID for all subsequent operations.
	id = existing.ID

	mu := e.NamespaceLock(existing.Namespace)
	mu.Lock()
	defer mu.Unlock()

	// Re-fetch inside the lock so concurrent deletes see the updated status.
	current, ok := e.GetGraph().GetNode(id)
	if !ok || current.Status == node.StatusSuperseded {
		return mcp.NewToolResultError(fmt.Sprintf("node %q not found", id)), nil
	}

	// Resolve superseded_by to its full ID if namespace prefix was omitted.
	if supersededBy != "" {
		if resolved, ok := e.ResolveNodeID(supersededBy); ok {
			supersededBy = resolved.ID
		}
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
	if err := e.GetGraph().UpsertNode(updated); err != nil {
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

// ---------------------------------------------------------------------------
// context_tag
// ---------------------------------------------------------------------------

// TagResult is the JSON response from context_tag.
type TagResult struct {
	Tag       string   `json:"tag"`
	TaggedIDs []string `json:"tagged_ids"`
	Count     int      `json:"count"`
}

// HandleContextTag bulk-tags nodes matching a semantic search query.
func (e *Engine) HandleContextTag(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := req.GetString("query", "")
	if query == "" {
		return mcp.NewToolResultError("query parameter is required"), nil
	}
	tag := strings.TrimSpace(req.GetString("tag", ""))
	if tag == "" {
		return mcp.NewToolResultError("tag parameter is required"), nil
	}
	namespace := req.GetString("namespace", "default")
	limit := req.GetInt("limit", 10)
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	// Embed the query.
	queryVec, err := embedWithContext(ctx, e.Embedder, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embed query: %v", err)), nil
	}

	// Search for matching nodes.
	results, err := e.EmbeddingStore.SearchActive(queryVec, limit, e.Embedder.Model())
	if err != nil {
		return mcp.NewToolResultJSON(TagResult{Tag: tag, TaggedIDs: []string{}, Count: 0})
	}

	mu := e.NamespaceLock(namespace)
	mu.Lock()
	defer mu.Unlock()

	var taggedIDs []string
	for _, r := range results {
		if err := ctx.Err(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		n, ok := e.GetGraph().GetNode(r.NodeID)
		if !ok {
			continue
		}

		// Filter to requested namespace.
		if !matchNamespace(n.Namespace, namespace) {
			continue
		}

		// Check if tag already exists on the node.
		hasTag := false
		for _, t := range n.Tags {
			if t == tag {
				hasTag = true
				break
			}
		}
		if hasTag {
			taggedIDs = append(taggedIDs, n.ID)
			continue
		}

		// Load from disk to get the full node (including body sections)
		// and avoid mutating the shared in-memory graph pointer directly.
		path := e.NodeStore.NodePath(n.ID)
		diskNode, err := e.NodeStore.LoadNode(path)
		if err != nil {
			continue
		}

		// Add the tag to the disk-loaded copy.
		diskNode.Tags = append(diskNode.Tags, tag)

		// Persist to disk first — if this fails, in-memory state stays clean.
		if err := e.NodeStore.SaveNode(diskNode); err != nil {
			continue
		}

		// Update in-memory graph only after successful disk write.
		_ = e.GetGraph().UpsertNode(diskNode)

		// Re-embed the node so semantic search accounts for the new tag.
		if e.Embedder != nil && e.EmbeddingStore != nil {
			tagStr := strings.Join(diskNode.Tags, " ")
			embedText := diskNode.Summary
			if tagStr != "" {
				embedText = diskNode.Summary + " " + tagStr
			}
			if diskNode.Context != "" {
				ctxSnip := diskNode.Context
				if len(ctxSnip) > 6000 {
					ctxSnip = ctxSnip[:6000]
				}
				embedText = embedText + "\n\n" + ctxSnip
			}
			if embedText != "" {
				if vec, embErr := embedWithContext(ctx, e.Embedder, embedText); embErr == nil {
					summaryHash := sha256Hex(embedText)
					_ = e.EmbeddingStore.Upsert(diskNode.ID, vec, summaryHash, e.Embedder.Model())
				}
			}
		}

		taggedIDs = append(taggedIDs, diskNode.ID)
	}

	result := TagResult{
		Tag:       tag,
		TaggedIDs: taggedIDs,
		Count:     len(taggedIDs),
	}
	return mcp.NewToolResultJSON(result)
}

// matchNamespace returns true if the node namespace matches the requested one.
// Treats empty namespace and "default" as equivalent.
func matchNamespace(nodeNS, requested string) bool {
	if nodeNS == requested {
		return true
	}
	if requested == "default" && nodeNS == "" {
		return true
	}
	if requested == "" && nodeNS == "default" {
		return true
	}
	return false
}

// sha256Hex returns the hex-encoded SHA-256 hash of s.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
