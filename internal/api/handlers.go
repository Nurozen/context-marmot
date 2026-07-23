package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	nspkg "github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/sdkgen"
	"github.com/nurozen/context-marmot/internal/summary"
	"github.com/nurozen/context-marmot/internal/verify"
	"github.com/nurozen/context-marmot/internal/warren"
)

// handleGraph returns the full graph for a namespace.
//
// Query params:
//
//	include_superseded=true  include superseded nodes (default: active only)
//	check_stale=true         compute source staleness per node (expensive)
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	if namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	includeSuperseded := r.URL.Query().Get("include_superseded") == "true"
	checkStale := r.URL.Query().Get("check_stale") == "true"

	var allNodes []*node.Node
	if includeSuperseded {
		allNodes = s.engine.GetGraph().AllNodes()
	} else {
		allNodes = s.engine.GetGraph().AllActiveNodes()
	}

	// Filter to the requested namespace.
	var filtered []*node.Node
	for _, n := range allNodes {
		if matchNamespace(n.Namespace, namespace) {
			filtered = append(filtered, n)
		}
	}

	projectRoot := filepath.Dir(s.engine.MarmotDir)

	resp := GraphResponse{
		Namespace: namespace,
		Nodes:     make([]APINode, 0, len(filtered)),
		Edges:     make([]APIEdge, 0),
	}

	// Build a set of node IDs in this namespace for filtering edges.
	nsNodeIDs := make(map[string]bool, len(filtered))
	for _, n := range filtered {
		nsNodeIDs[n.ID] = true
	}

	for _, n := range filtered {
		outEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Outbound)
		inEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Inbound)

		apiNode := nodeToAPI(n, len(outEdges)+len(inEdges))

		if checkStale && n.Source.Path != "" {
			status, err := verify.VerifyStaleness(n, projectRoot)
			if err == nil && status.IsStale {
				apiNode.IsStale = true
			}
		}

		resp.Nodes = append(resp.Nodes, apiNode)

		// Collect outbound edges, skipping cross-namespace edges whose
		// targets don't resolve to a node in this namespace view.
		for _, e := range outEdges {
			if !nsNodeIDs[e.Target] {
				continue
			}
			resp.Edges = append(resp.Edges, APIEdge{
				Source:   n.ID,
				Target:   e.Target,
				Relation: string(e.Relation),
				Class:    string(e.Class),
			})
		}
	}

	resp.NodeCount = len(resp.Nodes)
	resp.EdgeCount = len(resp.Edges)

	// Include heat pairs if available.
	if s.engine.HeatMap != nil {
		nodeIDs := make(map[string]bool, len(filtered))
		for _, n := range filtered {
			nodeIDs[n.ID] = true
		}
		// Use AllPairs() for thread-safe access to the pairs slice.
		for _, p := range s.engine.HeatMap.AllPairs() {
			if p.Weight < 0.06 {
				continue
			} // skip pairs at decay floor
			if nodeIDs[p.A] || nodeIDs[p.B] {
				resp.HeatPairs = append(resp.HeatPairs, APIHeatPair{
					A:      p.A,
					B:      p.B,
					Weight: p.Weight,
					Hits:   p.Hits,
					Last:   p.Last,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGraphAll returns the full graph across ALL namespaces, including
// cross-namespace bridge edges discovered via NSManager.
//
// Query params:
//
//	include_superseded=true  include superseded nodes (default: active only)
//	check_stale=true         compute source staleness per node (expensive)
func (s *Server) handleGraphAll(w http.ResponseWriter, r *http.Request) {
	includeSuperseded := r.URL.Query().Get("include_superseded") == "true"
	checkStale := r.URL.Query().Get("check_stale") == "true"

	var allNodes []*node.Node
	if includeSuperseded {
		allNodes = s.engine.GetGraph().AllNodes()
	} else {
		allNodes = s.engine.GetGraph().AllActiveNodes()
	}

	projectRoot := filepath.Dir(s.engine.MarmotDir)

	// Collect unique namespaces from nodes.
	nsSet := make(map[string]bool)
	for _, n := range allNodes {
		ns := n.Namespace
		if ns == "" {
			ns = "default"
		}
		nsSet[ns] = true
	}
	namespaces := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		namespaces = append(namespaces, ns)
	}

	// If NSManager is available, also include namespaces from the manager.
	for _, name := range s.engine.NamespaceNames() {
		if !nsSet[name] {
			nsSet[name] = true
			namespaces = append(namespaces, name)
		}
	}

	resp := GraphResponse{
		Namespace:  "_all",
		Nodes:      make([]APINode, 0, len(allNodes)),
		Edges:      make([]APIEdge, 0),
		Namespaces: namespaces,
	}

	// Build lookup maps for bridge detection:
	// nodeIDSet: quick existence check for edge targets
	// nodeNSMap: maps node ID → namespace for cross-namespace detection
	nodeIDSet := make(map[string]bool, len(allNodes))
	nodeNSMap := make(map[string]string, len(allNodes))
	for _, n := range allNodes {
		nodeIDSet[n.ID] = true
		nodeNSMap[n.ID] = n.Namespace
	}

	for _, n := range allNodes {
		outEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Outbound)
		inEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Inbound)

		apiNode := nodeToAPI(n, len(outEdges)+len(inEdges))

		if checkStale && n.Source.Path != "" {
			status, err := verify.VerifyStaleness(n, projectRoot)
			if err == nil && status.IsStale {
				apiNode.IsStale = true
			}
		}

		resp.Nodes = append(resp.Nodes, apiNode)

		// Collect outbound edges, classifying cross-namespace ones as "bridge".
		for _, e := range outEdges {
			target := e.Target
			edgeClass := string(e.Class)

			// Bridge detection strategy 1: target already exists as a known
			// node but is in a different namespace from the source node.
			if nodeIDSet[target] && nsSet != nil {
				targetNS := nodeNSMap[target]
				if targetNS != "" && targetNS != n.Namespace {
					edgeClass = "bridge"
				}
			}

			// Bridge detection strategy 2: target uses "namespace/nodeID"
			// format and doesn't match a known node — strip the prefix and
			// reclassify.
			if !nodeIDSet[target] && nsSet != nil {
				parts := strings.SplitN(target, "/", 2)
				if len(parts) == 2 && nsSet[parts[0]] && nodeIDSet[parts[1]] {
					target = parts[1]
					edgeClass = "bridge"
				}
			}

			resp.Edges = append(resp.Edges, APIEdge{
				Source:   n.ID,
				Target:   target,
				Relation: string(e.Relation),
				Class:    edgeClass,
			})
		}
	}

	resp.NodeCount = len(resp.Nodes)
	resp.EdgeCount = len(resp.Edges)

	// Include heat pairs if available.
	if s.engine.HeatMap != nil {
		// Use AllPairs() for thread-safe access to the pairs slice.
		for _, p := range s.engine.HeatMap.AllPairs() {
			if p.Weight < 0.06 {
				continue
			} // skip pairs at decay floor
			resp.HeatPairs = append(resp.HeatPairs, APIHeatPair{
				A:      p.A,
				B:      p.B,
				Weight: p.Weight,
				Hits:   p.Hits,
				Last:   p.Last,
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleNode returns a single node by namespace and ID.
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	id := r.PathValue("id")
	if namespace == "" || id == "" {
		writeError(w, http.StatusBadRequest, "namespace and id are required")
		return
	}

	// Construct the full node ID: namespace/id.
	fullID := namespace + "/" + id

	n, ok := s.engine.ResolveNodeID(fullID)
	if !ok {
		// Try the raw id without namespace prefix.
		n, ok = s.engine.ResolveNodeID(id)
		if !ok {
			writeError(w, http.StatusNotFound, "node not found: "+fullID)
			return
		}
	}

	outEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Outbound)
	inEdges := s.engine.GetGraph().GetEdges(n.ID, graph.Inbound)
	apiNode := nodeToAPI(n, len(outEdges)+len(inEdges))

	// Check staleness.
	if n.Source.Path != "" {
		projectRoot := filepath.Dir(s.engine.MarmotDir)
		status, err := verify.VerifyStaleness(n, projectRoot)
		if err == nil && status.IsStale {
			apiNode.IsStale = true
		}
	}

	writeJSON(w, http.StatusOK, apiNode)
}

// handleNodeUpdate applies partial updates to a node's summary and/or context.
func (s *Server) handleNodeUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	var req NodeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if req.Summary == "" && req.Context == "" && req.Tags == nil {
		writeError(w, http.StatusBadRequest, "at least one of summary, context, or tags must be provided")
		return
	}

	if strings.HasPrefix(id, "@") {
		s.handleWarrenNodeUpdate(w, id, req)
		return
	}

	n, ok := s.engine.ResolveNodeID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "node not found: "+id)
		return
	}

	mu := s.engine.NamespaceLock(n.Namespace)
	mu.Lock()
	defer mu.Unlock()

	// Load from disk to get the full node (including body sections).
	path := s.engine.NodeStore.NodePath(n.ID)
	diskNode, err := s.engine.NodeStore.LoadNode(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load node: "+err.Error())
		return
	}

	embeddingChanged := false
	if req.Summary != "" {
		diskNode.Summary = req.Summary
		embeddingChanged = true
	}
	if req.Context != "" {
		diskNode.Context = req.Context
		embeddingChanged = true
	}
	if req.Tags != nil {
		diskNode.Tags = *req.Tags
	}

	// Persist to disk.
	if err := s.engine.NodeStore.SaveNode(diskNode); err != nil {
		writeError(w, http.StatusInternalServerError, "save node: "+err.Error())
		return
	}

	// Update in-memory graph.
	if err := s.engine.GetGraph().UpsertNode(diskNode); err != nil {
		writeError(w, http.StatusInternalServerError, "graph upsert: "+err.Error())
		return
	}

	// Re-embed if summary changed.
	if embeddingChanged && s.engine.Embedder != nil {
		embedText := diskNode.Summary
		if diskNode.Context != "" {
			ctxSnip := diskNode.Context
			if len(ctxSnip) > 6000 {
				ctxSnip = ctxSnip[:6000]
			}
			embedText = diskNode.Summary + "\n\n" + ctxSnip
		}
		if embedText != "" {
			vec, err := s.engine.Embedder.Embed(embedText)
			if err == nil {
				h := sha256.Sum256([]byte(embedText))
				summaryHash := hex.EncodeToString(h[:])
				_ = s.engine.EmbeddingStore.Upsert(diskNode.ID, vec, summaryHash, s.engine.Embedder.Model())
			}
		}
	}

	nodeHash := verify.ComputeNodeHash(diskNode)

	writeJSON(w, http.StatusOK, NodeUpdateResponse{
		NodeID: diskNode.ID,
		Hash:   nodeHash,
		Status: "updated",
	})
}

func (s *Server) handleWarrenNodeUpdate(w http.ResponseWriter, id string, req NodeUpdateRequest) {
	vaultID, localID, ok := warren.SplitQualifiedVaultID(id)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid Warren node id: "+id)
		return
	}
	// Self-alias guard: an @-write to the workspace's own vault ID would land
	// in the warren checkout copy and split-brain the live vault (including
	// legacy state that still records the self project as editable).
	if vaultID != "" && vaultID == s.engine.LocalVaultID {
		writeError(w, http.StatusForbidden, fmt.Sprintf("vault %q is this workspace's own vault; update the node via PUT /api/node/%s (no @ prefix)", vaultID, localID))
		return
	}
	mount, ok := s.findWarrenMountByVault(vaultID)
	if !ok {
		writeError(w, http.StatusNotFound, "Warren mount not found for vault: "+vaultID)
		return
	}
	// Rejected-write copy (plan §9.3, same wording family as the MCP path):
	// name the mode and the den verb; author read-only is a veto with no
	// remediation.
	if !mount.Editable {
		if warrenAuthorReadOnly(mount) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("write rejected: the warren author marked project %q read-only (readonly: true in the warren manifest) — author veto, edits must go through the warren repository itself", mount.ProjectID))
			return
		}
		writeError(w, http.StatusForbidden, fmt.Sprintf("write rejected: vault %q is a read-only warren mount in this workspace — link it editable: 'marmot den link <den> --edit %s/%s'", vaultID, mount.WarrenID, mount.ProjectID))
		return
	}

	// Serialize the load-modify-save cycle on the same per-mount lock as the
	// MCP @-write path (handleWarrenContextWrite): C8 makes the payloads
	// equivalent, but without shared locking a concurrent API+MCP (or
	// API+API) update to one mounted node would interleave and silently drop
	// one writer's summary/context/tags.
	mu := s.engine.NamespaceLock("@" + vaultID)
	mu.Lock()
	defer mu.Unlock()

	store := node.NewStore(mount.Path)
	path := store.NodePath(localID)
	diskNode, err := store.LoadNode(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found: "+id)
		return
	}

	embeddingChanged := false
	if req.Summary != "" {
		diskNode.Summary = req.Summary
		embeddingChanged = true
	}
	if req.Context != "" {
		diskNode.Context = req.Context
		embeddingChanged = true
	}
	if req.Tags != nil {
		diskNode.Tags = *req.Tags
	}

	// warren.WriteEditableNode is the single MCP/API write-back path: the node
	// save error is fatal, while an embedding failure must not roll the
	// durable node write back or 500 the request — the response carries a
	// warning field (and stderr gets a line) so a stale embedding is never
	// silent.
	var warning string
	var vec []float32
	var summaryHash, model string
	if embeddingChanged && s.engine.Embedder != nil {
		embedText := warren.EmbedText(diskNode)
		if embedText != "" {
			v, err := s.engine.Embedder.Embed(embedText)
			if err != nil {
				warning = "embedding not updated: " + err.Error()
			} else {
				vec = v
				h := sha256.Sum256([]byte(embedText))
				summaryHash = hex.EncodeToString(h[:])
				model = s.engine.Embedder.Model()
			}
		}
	}
	writeWarning, err := warren.WriteEditableNode(mount, diskNode, vec, summaryHash, model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save Warren node: "+err.Error())
		return
	}
	if warning == "" {
		warning = writeWarning
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "warning: warren editable write %s: %s\n", id, warning)
	}
	if s.engine.VaultRegistry != nil {
		if err := s.engine.VaultRegistry.Refresh(vaultID); err != nil && !errors.Is(err, nspkg.ErrNotLoaded) {
			// ErrNotLoaded means nothing was cached — nothing to refresh.
			// Anything else makes the stale-cache window visible, not silent.
			fmt.Fprintf(os.Stderr, "warning: refresh after editable write failed for vault %q: %v\n", vaultID, err)
		}
	}

	writeJSON(w, http.StatusOK, NodeUpdateResponse{
		NodeID:  id,
		Hash:    verify.ComputeNodeHash(diskNode),
		Status:  "updated",
		Warning: warning,
	})
}

// handleSearch performs semantic search across the knowledge graph.
//
// Query params:
//
//	q       search query (required)
//	ns      namespace filter (optional)
//	limit   max results (default 10)
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	ns := r.URL.Query().Get("ns")
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	if s.engine.Embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "embedding service not available")
		return
	}

	vec, err := s.engine.Embedder.Embed(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "embed query: "+err.Error())
		return
	}

	results, err := s.engine.EmbeddingStore.Search(vec, limit, s.engine.Embedder.Model())
	if err != nil {
		// Graceful degradation: empty results on model mismatch or empty store.
		results = []embedding.ScoredResult{}
	}
	results = append(results, s.searchMountedVaults(r.Context(), query, vec, ns, limit)...)
	results = append(results, s.searchDenLinkedVaults(r.Context(), query, vec, ns, limit)...)
	results = dedupeAndRankSearchResults(results, limit)

	resp := SearchResponse{Results: make([]SearchResult, 0, len(results))}
	for _, sr := range results {
		if strings.HasPrefix(ns, "_warren/") && !strings.HasPrefix(sr.NodeID, "@") {
			continue
		}
		n, provenance, ok := s.resolveSearchNode(sr.NodeID)
		if !ok {
			continue
		}
		// Apply optional namespace filter.
		if ns != "" && !strings.HasPrefix(ns, "_warren/") && !matchNamespace(n.Namespace, ns) {
			continue
		}
		resp.Results = append(resp.Results, SearchResult{
			NodeID:     sr.NodeID,
			Score:      sr.Score,
			Summary:    n.Summary,
			Type:       n.Type,
			Namespace:  n.Namespace,
			Provenance: provenance,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// searchMountedVaults fans a warren-scoped search across mounted vaults.
// Remote vaults are searched with a query vector in THAT vault's embedding
// model (per-vault-model federation, mirroring context_query): the local
// vector is reused when models match, otherwise the vault's own embedder
// re-embeds the query, so a den-link vault built with a different model no
// longer silently returns nothing. The seeded local vector is used directly
// for the local self-alias store (it always carries the local model).
func (s *Server) searchMountedVaults(ctx context.Context, query string, vec []float32, ns string, limit int) []embedding.ScoredResult {
	if s.engine.VaultRegistry == nil {
		return nil
	}
	warrenFilter := strings.TrimPrefix(ns, "_warren/")
	if warrenFilter == ns {
		return nil
	}
	mounts, err := warren.ActiveMounts(s.engine.MarmotDir)
	if err != nil {
		s.warnVaultOnce("_active_mounts", "warren mounts unavailable for search: %v", err)
	}
	mountByVault := make(map[string]warren.ProjectStatus, len(mounts))
	for _, mount := range mounts {
		if !mount.Available || mount.VaultID == "" {
			continue
		}
		if warrenFilter != "" && mount.WarrenID != warrenFilter {
			continue
		}
		mountByVault[mount.VaultID] = mount
	}
	if len(mountByVault) == 0 {
		return nil
	}

	remoteState := s.engine.NewRemoteQueryState(vec)

	var results []embedding.ScoredResult
	for vaultID, mount := range mountByVault {
		if vaultID == "" {
			continue
		}
		if vaultID == s.engine.LocalVaultID {
			// A self-alias project is served from the live vault: search the
			// local store directly (the registry must never resolve our own
			// vault) so the project stays visible in its warren's scope.
			if !mount.SelfAlias {
				continue // stale engine cache or hand-edited state: stay skipped
			}
			localResults, err := s.engine.EmbeddingStore.SearchActive(vec, limit, s.engine.Embedder.Model())
			if err != nil {
				s.warnVaultOnce(vaultID, "local vault search failed for warren scope: %v", err)
				continue
			}
			for _, result := range localResults {
				results = append(results, embedding.ScoredResult{
					NodeID: "@" + vaultID + "/" + result.NodeID,
					Score:  result.Score,
				})
			}
			continue
		}
		remoteStore, err := s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)
		if err != nil {
			// Best-effort: local results still return, but the degradation is
			// visible (once per vault) instead of silently vanishing.
			s.warnVaultOnce(vaultID, "warren vault %q embedding store unavailable, excluded from search: %v", vaultID, err)
			continue
		}
		searchVec, searchModel, ok := s.engine.RemoteQueryVector(ctx, vaultID, remoteStore, query, remoteState)
		if !ok {
			continue // already warned (model mismatch / embedder failure)
		}
		remoteResults, err := remoteStore.SearchActive(searchVec, limit, searchModel)
		if err != nil {
			s.warnVaultOnce(vaultID, "warren vault %q search failed, excluded from results: %v", vaultID, err)
			continue
		}
		for _, result := range remoteResults {
			results = append(results, embedding.ScoredResult{
				NodeID: "@" + vaultID + "/" + result.NodeID,
				Score:  result.Score,
			})
		}
	}
	return results
}

// searchDenLinkedVaults fans the search across the served den's resolved
// _den.md link vaults, mirroring context_query's federation (the MCP path
// searches every registry vault; /api/search previously only reached remote
// vaults under an explicit _warren/ scope, so den-linked results were silently
// missing from the UI). Warren-scoped searches (ns=_warren/…) are excluded —
// that scope means "this warren's mounts", which searchMountedVaults covers.
// Per-vault-model federation is identical to the mount path: the local vector
// is reused when models match, otherwise the vault's own embedder re-embeds.
func (s *Server) searchDenLinkedVaults(ctx context.Context, query string, vec []float32, ns string, limit int) []embedding.ScoredResult {
	if s.engine.VaultRegistry == nil || strings.HasPrefix(ns, "_warren/") {
		return nil
	}
	vaultIDs := s.engine.DenLinkedVaultIDs()
	if len(vaultIDs) == 0 {
		return nil
	}
	remoteState := s.engine.NewRemoteQueryState(vec)
	var results []embedding.ScoredResult
	for _, vaultID := range vaultIDs {
		remoteStore, err := s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)
		if err != nil {
			// Best-effort: local results still return, but the degradation is
			// visible (once per vault) instead of silently vanishing.
			s.warnVaultOnce(vaultID, "den-linked vault %q embedding store unavailable, excluded from search: %v", vaultID, err)
			continue
		}
		searchVec, searchModel, ok := s.engine.RemoteQueryVector(ctx, vaultID, remoteStore, query, remoteState)
		if !ok {
			continue // already warned (model mismatch / embedder failure)
		}
		remoteResults, err := remoteStore.SearchActive(searchVec, limit, searchModel)
		if err != nil {
			s.warnVaultOnce(vaultID, "den-linked vault %q search failed, excluded from results: %v", vaultID, err)
			continue
		}
		for _, result := range remoteResults {
			results = append(results, embedding.ScoredResult{
				NodeID: "@" + vaultID + "/" + result.NodeID,
				Score:  result.Score,
			})
		}
	}
	return results
}

func (s *Server) resolveSearchNode(id string) (*node.Node, *warren.Provenance, bool) {
	if !strings.HasPrefix(id, "@") {
		n, ok := s.engine.GetGraph().GetNode(id)
		return n, nil, ok
	}
	vaultID, nodeID, ok := warren.SplitQualifiedVaultID(id)
	if !ok || s.engine.VaultRegistry == nil {
		return nil, nil, false
	}
	// The workspace's own vault ID resolves against the live graph (zero
	// staleness, no registry copy) with alias provenance; edits go through
	// the unqualified local node, so the @-qualified view stays read-only.
	if vaultID != "" && vaultID == s.engine.LocalVaultID {
		n, ok := s.engine.GetGraph().GetNode(nodeID)
		if !ok {
			return nil, nil, false
		}
		return n, &warren.Provenance{
			Source:      "local_alias",
			VaultID:     vaultID,
			MarmotDir:   s.engine.MarmotDir,
			QualifiedID: id,
			Editable:    false, // edit via the unqualified local node, not @-writes
		}, true
	}
	n, ok := s.engine.VaultRegistry.Resolve(vaultID, nodeID)
	if !ok {
		return nil, nil, false
	}
	mount, mountOK := s.findWarrenMountByVault(vaultID)
	if !mountOK {
		return n, nil, true
	}
	return n, &warren.Provenance{
		Source:      "warren_mount",
		WarrenID:    mount.WarrenID,
		ProjectID:   mount.ProjectID,
		VaultID:     mount.VaultID,
		MarmotDir:   mount.Path,
		QualifiedID: id,
		Editable:    mount.Editable,
	}, true
}

func dedupeAndRankSearchResults(results []embedding.ScoredResult, limit int) []embedding.ScoredResult {
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	seen := make(map[string]bool, len(results))
	out := make([]embedding.ScoredResult, 0, min(limit, len(results)))
	for _, result := range results {
		if seen[result.NodeID] {
			continue
		}
		seen[result.NodeID] = true
		out = append(out, result)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// handleHeat returns heat map pairs for a namespace.
func (s *Server) handleHeat(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	if namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	if s.engine.HeatMap == nil {
		writeJSON(w, http.StatusOK, struct {
			Pairs []APIHeatPair `json:"pairs"`
		}{Pairs: []APIHeatPair{}})
		return
	}

	// Collect all active node IDs in this namespace to filter pairs.
	allNodes := s.engine.GetGraph().AllActiveNodes()
	nsIDs := make(map[string]bool)
	for _, n := range allNodes {
		if matchNamespace(n.Namespace, namespace) {
			nsIDs[n.ID] = true
		}
	}

	var pairs []APIHeatPair
	for _, p := range s.engine.HeatMap.AllPairs() {
		if p.Weight < 0.06 {
			continue
		} // skip pairs at decay floor
		if nsIDs[p.A] || nsIDs[p.B] {
			pairs = append(pairs, APIHeatPair{
				A:      p.A,
				B:      p.B,
				Weight: p.Weight,
				Hits:   p.Hits,
				Last:   p.Last,
			})
		}
	}
	if pairs == nil {
		pairs = []APIHeatPair{}
	}

	writeJSON(w, http.StatusOK, struct {
		Pairs []APIHeatPair `json:"pairs"`
	}{Pairs: pairs})
}

// handleNamespaces lists all known namespaces with node counts.
func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	resp := NamespacesResponse{Namespaces: []NamespaceInfo{}}

	nsCounts := make(map[string]int)
	for _, n := range s.engine.GetGraph().AllActiveNodes() {
		ns := n.Namespace
		if ns == "" {
			ns = "default"
		}
		nsCounts[ns]++
	}

	if len(nsCounts) == 0 {
		nsCounts["default"] = 0
	}

	for _, name := range s.engine.NamespaceNames() {
		if _, ok := nsCounts[name]; !ok {
			nsCounts[name] = 0
		}
	}

	names := make([]string, 0, len(nsCounts))
	for name := range nsCounts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		hasSummary := false
		if _, err := summary.ReadSummary(s.engine.MarmotDir, name); err == nil {
			hasSummary = true
		}
		resp.Namespaces = append(resp.Namespaces, NamespaceInfo{
			Name:       name,
			NodeCount:  nsCounts[name],
			HasSummary: hasSummary,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleBridges lists all bridge manifests.
func (s *Server) handleBridges(w http.ResponseWriter, r *http.Request) {
	resp := BridgesResponse{Bridges: []BridgeInfo{}}

	bridges, crossVaultBridges := s.engine.BridgeSnapshot()
	if len(bridges) == 0 && len(crossVaultBridges) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	for _, b := range bridges {
		resp.Bridges = append(resp.Bridges, BridgeInfo{
			Source:           b.Source,
			Target:           b.Target,
			AllowedRelations: b.AllowedRelations,
			IsCrossVault:     b.IsCrossVault(),
		})
	}

	for _, b := range crossVaultBridges {
		// Avoid duplicates: cross-vault bridges already in Bridges map are skipped.
		alreadyIncluded := false
		for _, existing := range resp.Bridges {
			if existing.Source == b.Source && existing.Target == b.Target {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			resp.Bridges = append(resp.Bridges, BridgeInfo{
				Source:           b.Source,
				Target:           b.Target,
				AllowedRelations: b.AllowedRelations,
				IsCrossVault:     true,
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleSummary returns the namespace-level summary.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	if namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	result, err := summary.ReadSummary(s.engine.MarmotDir, namespace)
	if err != nil {
		writeError(w, http.StatusNotFound, "summary not found for namespace: "+namespace)
		return
	}

	writeJSON(w, http.StatusOK, SummaryResponse{
		Namespace:   result.Namespace,
		Content:     result.Content,
		NodeCount:   result.NodeCount,
		GeneratedAt: result.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// handleWarrens lists Warren registrations for the current local workspace,
// each with its computed identified projects (identity is derived from
// vault_id at read time, never stored in workspace state).
func (s *Server) handleWarrens(w http.ResponseWriter, r *http.Request) {
	state, _, err := warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren state: "+err.Error())
		return
	}
	identified := make(map[string][]string)
	if mounts, mountsErr := warren.ActiveMounts(s.engine.MarmotDir); mountsErr == nil {
		for _, mount := range mounts {
			if mount.SelfAlias {
				identified[mount.WarrenID] = append(identified[mount.WarrenID], mount.ProjectID)
			}
		}
	}
	warrens := make(map[string]WarrenEntry, len(state.Warrens))
	for id, entry := range state.Warrens {
		active := entry.ActiveProjects
		if active == nil {
			// JSON shape stability: emit [] instead of dropping the key.
			active = []string{}
		}
		warrens[id] = WarrenEntry{
			WorkspaceWarren:    entry,
			ActiveProjects:     active,
			IdentifiedProjects: identified[id],
			Reachable:          dirExists(entry.Path),
		}
	}
	denLinks := s.engine.DenLinkStatuses()
	if denLinks == nil {
		// JSON shape stability: always an array, never null/absent.
		denLinks = []mcpserver.DenLinkStatus{}
	}
	writeJSON(w, http.StatusOK, WarrensResponse{
		Warrens:      warrens,
		LocalVaultID: s.engine.LocalVaultID,
		DenLinks:     denLinks,
	})
}

// handleWarrenStatus returns mounted/editable status for a single Warren.
func (s *Server) handleWarrenStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "warren id is required")
		return
	}
	workspaceRoot := filepath.Dir(s.engine.MarmotDir)
	state, _, err := warren.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren state: "+err.Error())
		return
	}
	entry, ok := state.Warrens[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Warren not registered: "+id)
		return
	}
	statuses, err := warren.Status(workspaceRoot, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, WarrenStatusResponse{
		WarrenID: id,
		Path:     entry.Path,
		Projects: statuses,
	})
}

// handleWarrenGraph returns a graph view across active projects in one Warren.
func (s *Server) handleWarrenGraph(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "warren id is required")
		return
	}
	state, _, err := warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren state: "+err.Error())
		return
	}
	entry, ok := state.Warrens[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Warren not registered: "+id)
		return
	}
	mounts, err := warren.ActiveMounts(s.engine.MarmotDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren mounts: "+err.Error())
		return
	}

	resp := GraphResponse{
		Namespace: "_warren/" + id,
		Nodes:     []APINode{},
		Edges:     []APIEdge{},
	}

	// An unreachable warren (checkout moved/deleted, no burrow cache) yields
	// zero mounts from ActiveMounts, which used to render as a clean empty
	// 200 graph — silent exactly when the user most needs a signal (C2).
	// Surface every active project as skipped with the manifest error so the
	// UI can toast and the panel rows carry skip tooltips.
	if !entry.Materialized {
		if _, _, merr := warren.LoadManifest(entry.Path); merr != nil {
			for _, projectID := range entry.ActiveProjects {
				markSkipped(&resp, projectID, fmt.Sprintf("warren unreachable: manifest unreadable at %s: %v", entry.Path, merr))
			}
		}
	}
	nsSet := make(map[string]bool)
	renderedVaults := make(map[string]bool)

	for _, mount := range mounts {
		if mount.WarrenID != id {
			continue
		}
		// Node IDs are keyed by vault (@<vault_id>/<node>), so each vault
		// renders once. Two identified projects sharing the workspace
		// vault_id are coherent per the alias contract — a second pass over
		// the live vault would duplicate every node and edge.
		if mount.VaultID != "" && renderedVaults[mount.VaultID] {
			continue
		}
		if !mount.Available {
			fmt.Fprintf(os.Stderr, "warning: warren %q project %q unavailable at %s, skipped from graph\n", id, mount.ProjectID, mount.Path)
			markSkipped(&resp, mount.ProjectID, fmt.Sprintf("project unavailable at %s", mount.Path))
			continue
		}
		// A self-alias serves from the live workspace vault, never the warren
		// snapshot; its @-qualified view stays read-only (edits go through
		// the unqualified local node).
		storeDir, provenanceSource, provenanceDir, provenanceEditable := mount.Path, "warren_mount", mount.Path, mount.Editable
		if mount.SelfAlias {
			storeDir, provenanceSource, provenanceDir, provenanceEditable = s.engine.MarmotDir, "local_alias", s.engine.MarmotDir, false
		}
		store := node.NewStore(storeDir)
		g, err := graph.LoadGraph(store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: warren %q project %q graph unreadable: %v (skipped from graph)\n", id, mount.ProjectID, err)
			markSkipped(&resp, mount.ProjectID, fmt.Sprintf("graph unreadable: %v", err))
			continue
		}
		renderedVaults[mount.VaultID] = true
		nodes := g.AllActiveNodes()
		for _, n := range nodes {
			outEdges := g.GetEdges(n.ID, graph.Outbound)
			inEdges := g.GetEdges(n.ID, graph.Inbound)
			apiNode := nodeToAPI(n, len(outEdges)+len(inEdges))
			apiNode.ID = "@" + mount.VaultID + "/" + n.ID
			apiNode.Provenance = &warren.Provenance{
				Source:      provenanceSource,
				WarrenID:    mount.WarrenID,
				ProjectID:   mount.ProjectID,
				VaultID:     mount.VaultID,
				MarmotDir:   provenanceDir,
				QualifiedID: apiNode.ID,
				Editable:    provenanceEditable,
			}
			for i, edge := range apiNode.Edges {
				edge.Source = apiNode.ID
				if !strings.HasPrefix(edge.Target, "@") {
					edge.Target = "@" + mount.VaultID + "/" + edge.Target
				}
				apiNode.Edges[i] = edge
			}
			resp.Nodes = append(resp.Nodes, apiNode)
			if n.Namespace != "" {
				nsSet[mount.ProjectID+":"+n.Namespace] = true
			}
			for _, e := range outEdges {
				target := e.Target
				if !strings.HasPrefix(target, "@") {
					target = "@" + mount.VaultID + "/" + target
				}
				resp.Edges = append(resp.Edges, APIEdge{
					Source:   apiNode.ID,
					Target:   target,
					Relation: string(e.Relation),
					Class:    string(e.Class),
				})
			}
		}
	}

	resp.NodeCount = len(resp.Nodes)
	resp.EdgeCount = len(resp.Edges)
	for ns := range nsSet {
		resp.Namespaces = append(resp.Namespaces, ns)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleWarrenRefresh reloads the engine's warren state (routes, mounts,
// runtime bridges, vault registry) from disk. The reload is engine-global,
// not per-warren — mounts/bridges/routes are one composite state and a
// partial reload would reintroduce split views; the {id} is validated and
// echoed so callers can gate on a specific warren being registered.
func (s *Server) handleWarrenRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "warren id is required")
		return
	}
	state, _, err := warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren state: "+err.Error())
		return
	}
	if _, ok := state.Warrens[id]; !ok {
		writeError(w, http.StatusNotFound, "Warren not registered: "+id)
		return
	}
	if err := s.engine.ReloadWarrenAndDenState(); err != nil {
		writeError(w, http.StatusInternalServerError, "warren refresh: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"warren_id": id,
		"status":    "reloaded",
	})
}

// dirExists mirrors the CLI 'warren list' REACHABLE computation: the
// registered checkout directory exists on disk.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// markSkipped records a Warren graph skip with its reason (the same reason
// the stderr warning carries, U4.4) so UIs can render it as a tooltip.
func markSkipped(resp *GraphResponse, projectID, reason string) {
	resp.Skipped = append(resp.Skipped, projectID)
	if resp.SkippedReasons == nil {
		resp.SkippedReasons = make(map[string]string)
	}
	resp.SkippedReasons[projectID] = reason
}

// handleWarrenMount mounts warren projects into the workspace over HTTP.
// The handler is thin: warren.Mount owns validation and refusal messages
// (collisions, unknown projects, identity no-ops), which pass through as
// 400s so the UI gets the same message quality as the CLI. Materialized
// mounts are never offered over HTTP (heavy IO + cache lifecycle).
func (s *Server) handleWarrenMount(w http.ResponseWriter, r *http.Request) {
	s.handleWarrenMountChange(w, r, true)
}

// handleWarrenUnmount unmounts warren projects from the workspace over HTTP.
func (s *Server) handleWarrenUnmount(w http.ResponseWriter, r *http.Request) {
	s.handleWarrenMountChange(w, r, false)
}

func (s *Server) handleWarrenMountChange(w http.ResponseWriter, r *http.Request, mount bool) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "warren id is required")
		return
	}
	var req WarrenMountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	state, _, err := warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load Warren state: "+err.Error())
		return
	}
	entry, ok := state.Warrens[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Warren not registered: "+id)
		return
	}
	action := "unmounted"
	if mount {
		action = "mounted"
	}
	projects := req.Projects
	if req.All {
		if mount {
			manifest, _, manifestErr := warren.LoadManifest(entry.Path)
			if manifestErr != nil {
				writeError(w, http.StatusInternalServerError, "load Warren manifest: "+manifestErr.Error())
				return
			}
			projects = make([]string, 0, len(manifest.Projects))
			for _, project := range manifest.Projects {
				projects = append(projects, project.ProjectID)
			}
		} else {
			projects = append([]string(nil), entry.ActiveProjects...)
		}
	}
	if len(projects) == 0 {
		if req.All {
			// Nothing to do (e.g. unmount --all with nothing mounted): a
			// no-op, not an error.
			writeJSON(w, http.StatusOK, WarrenMountResponse{
				WarrenID: id, Action: action, Projects: []string{}, Status: "reloaded",
			})
			return
		}
		writeError(w, http.StatusBadRequest, `no projects specified: provide "projects" or set "all": true`)
		return
	}
	workspaceRoot := filepath.Dir(s.engine.MarmotDir)
	if mount {
		_, err = warren.Mount(workspaceRoot, id, projects, false) // never materialize (burrow) over HTTP
	} else {
		_, err = warren.Unmount(workspaceRoot, id, projects)
	}
	if err != nil {
		// Warren-layer refusals (vault-ID collisions, unknown projects,
		// not-mounted, editable self-alias) pass through verbatim.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.engine.ReloadWarrenAndDenState(); err != nil {
		writeError(w, http.StatusInternalServerError, "warren reload after "+action+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, WarrenMountResponse{
		WarrenID: id,
		Action:   action,
		Projects: projects,
		Status:   "reloaded",
	})
}

// handleDoctorWorkspace returns the workspace-level warren doctor report
// verbatim — the same JSON shape as `marmot warren doctor --workspace`
// (severity/code/message issues incl. self_identity, self_alias_* and
// vault_id_collision_workspace).
func (s *Server) handleDoctorWorkspace(w http.ResponseWriter, r *http.Request) {
	report, err := warren.DoctorWorkspace(s.engine.MarmotDir, filepath.Dir(s.engine.MarmotDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "warren doctor: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) findWarrenMountByVault(vaultID string) (warren.ProjectStatus, bool) {
	mounts, err := warren.ActiveMounts(s.engine.MarmotDir)
	if err != nil {
		// Behavior unchanged (callers report "mount not found") but the real
		// cause is no longer silent.
		fmt.Fprintf(os.Stderr, "warning: warren mounts unavailable while resolving vault %q: %v\n", vaultID, err)
		return warren.ProjectStatus{}, false
	}
	for _, mount := range mounts {
		if mount.VaultID == vaultID {
			return mount, true
		}
	}
	return warren.ProjectStatus{}, false
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

// handleVersion returns the current graph version counter (live-reload) and
// the marmot build version.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, VersionResponse{
		Version:    s.version.Load(),
		AppVersion: s.appVersion,
	})
}

// handleSSE streams Server-Sent Events to the client. When the graph version
// changes (due to file watcher detecting disk changes), a "graph-changed"
// event is pushed to all connected clients.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Register this client.
	ch := make(chan struct{}, 1)
	s.sseClients.Store(ch, true)
	defer func() {
		s.sseClients.Delete(ch)
		close(ch)
	}()

	// Send initial version.
	fmt.Fprintf(w, "data: {\"version\":%d}\n\n", s.version.Load())
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: graph-changed\ndata: {\"version\":%d}\n\n", s.version.Load())
			flusher.Flush()
		}
	}
}

// NotifyChange bumps the version counter and notifies all SSE clients.
// Called by the file watcher when vault files change on disk.
func (s *Server) NotifyChange() {
	s.version.Add(1)
	s.sseClients.Range(func(key, _ any) bool {
		ch := key.(chan struct{})
		select {
		case ch <- struct{}{}:
		default: // don't block if client is slow
		}
		return true
	})
}

// nodeToAPI converts a domain node to its API representation.
func nodeToAPI(n *node.Node, edgeCount int) APINode {
	tags := n.Tags
	if tags == nil {
		tags = []string{}
	}
	apiNode := APINode{
		ID:           n.ID,
		Type:         n.Type,
		Namespace:    n.Namespace,
		Status:       n.Status,
		ValidFrom:    n.ValidFrom,
		ValidUntil:   n.ValidUntil,
		SupersededBy: n.SupersededBy,
		Summary:      n.Summary,
		Context:      n.Context,
		EdgeCount:    edgeCount,
		Edges:        make([]APIEdge, 0, len(n.Edges)),
		Tags:         tags,
	}

	if n.Source.Path != "" {
		apiNode.Source = &APISource{
			Path:  n.Source.Path,
			Lines: n.Source.Lines,
			Hash:  n.Source.Hash,
		}
	}

	for _, e := range n.Edges {
		apiNode.Edges = append(apiNode.Edges, APIEdge{
			Source:   n.ID,
			Target:   e.Target,
			Relation: string(e.Relation),
			Class:    string(e.Class),
		})
	}

	return apiNode
}

// ---------------------------------------------------------------------------
// SDK endpoints
// ---------------------------------------------------------------------------

// handleSDKTS serves the generated TypeScript SDK file.
func (s *Server) handleSDKTS(w http.ResponseWriter, r *http.Request) {
	// Construct base URL from the request's Host header.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	baseURL := scheme + "://" + r.Host

	content := sdkgen.Generate(baseURL)

	w.Header().Set("Content-Type", "text/typescript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="marmot-sdk.ts"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, content)
}

// handleSDKCall bridges TypeScript SDK tool calls to the MCP engine handlers.
// It reads a JSON body, constructs an mcp.CallToolRequest, delegates to the
// appropriate engine handler, and returns the result as JSON.
func (s *Server) handleSDKCall(w http.ResponseWriter, r *http.Request) {
	tool := r.PathValue("tool")
	if tool == "" {
		writeError(w, http.StatusBadRequest, "tool name is required")
		return
	}

	// Read and parse JSON body into a generic map for arguments.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body: "+err.Error())
		return
	}
	defer r.Body.Close()

	var args map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	// Construct an mcp.CallToolRequest with the arguments map.
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      tool,
			Arguments: args,
		},
	}

	ctx := context.Background()

	// Dispatch to the appropriate engine handler.
	var result *mcp.CallToolResult
	var handlerErr error

	switch tool {
	case "context_query":
		result, handlerErr = s.engine.HandleContextQuery(ctx, req)
	case "context_write":
		result, handlerErr = s.engine.HandleContextWrite(ctx, req)
	case "context_verify":
		result, handlerErr = s.engine.HandleContextVerify(ctx, req)
	case "context_delete":
		result, handlerErr = s.engine.HandleContextDelete(ctx, req)
	case "context_tag":
		result, handlerErr = s.engine.HandleContextTag(ctx, req)
	default:
		writeError(w, http.StatusNotFound, "unknown tool: "+tool)
		return
	}

	if handlerErr != nil {
		writeError(w, http.StatusInternalServerError, "tool error: "+handlerErr.Error())
		return
	}

	// Extract text content from the CallToolResult.
	// The engine handlers return results via NewToolResultText or NewToolResultJSON,
	// both of which place a TextContent as the first element.
	if result == nil {
		writeError(w, http.StatusInternalServerError, "tool returned nil result")
		return
	}

	if result.IsError {
		// Tool-level error: extract the error message from content.
		msg := "unknown tool error"
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	// For successful results, extract the text content.
	// If StructuredContent is present (from NewToolResultJSON), return it directly.
	if result.StructuredContent != nil {
		writeJSON(w, http.StatusOK, result.StructuredContent)
		return
	}

	// Otherwise extract text from Content slice.
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			// Try to parse as JSON first (NewToolResultJSON puts JSON text in content).
			var parsed any
			if err := json.Unmarshal([]byte(tc.Text), &parsed); err == nil {
				writeJSON(w, http.StatusOK, parsed)
				return
			}
			// Not JSON — return as a context string (e.g., query XML result).
			writeJSON(w, http.StatusOK, map[string]string{"context": tc.Text})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

// handleChatUndo pops the most recent undo entry for a session and restores
// the pre-mutation state: existing nodes are restored via SaveNode + UpsertNode,
// and nodes that were created by the mutation are deleted.
func (s *Server) handleChatUndo(w http.ResponseWriter, r *http.Request) {
	var req ChatUndoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	// Per-row undo from the code-mode audit trail passes undo_id so a
	// specific mutation can be reversed out of LIFO order. Without an
	// undo_id, fall back to popping the most recent entry.
	var entry *curator.UndoEntry
	if req.UndoID != "" {
		var blocked bool
		entry, blocked = s.undoStack.PopLatestByID(req.SessionID, req.UndoID)
		if blocked {
			writeError(w, http.StatusConflict, "undo entry is not the most recent change; undo newer changes first")
			return
		}
	} else {
		entry = s.undoStack.Pop(req.SessionID)
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "no undo entries for session")
		return
	}

	restored := 0

	// Restore snapshots of nodes that existed before the mutation.
	for _, snap := range entry.Snapshots {
		if snap.Existed && snap.Node != nil {
			if err := s.engine.NodeStore.SaveNode(snap.Node); err != nil {
				continue
			}
			_ = s.engine.GetGraph().UpsertNode(snap.Node)
			restored++
		} else if !snap.Existed && snap.Node != nil {
			// Node was created by the mutation — delete it.
			_ = s.engine.NodeStore.DeleteNode(snap.Node.ID)
			_ = s.engine.GetGraph().RemoveNode(snap.Node.ID)
			restored++
		}
	}

	// Delete nodes listed in Created (node IDs created by mutation).
	for _, id := range entry.Created {
		_ = s.engine.NodeStore.DeleteNode(id)
		_ = s.engine.GetGraph().RemoveNode(id)
		restored++
	}

	s.NotifyChange()

	writeJSON(w, http.StatusOK, ChatUndoResponse{
		Restored: restored,
		UndoID:   entry.ID,
	})
}

// handleSuggestions runs the curation suggestions engine and returns paginated
// results. Query params: ns (namespace filter), limit (default 20), offset,
// check_stale (expensive staleness check).
func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("ns")
	checkStale := r.URL.Query().Get("check_stale") == "true"

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	g := s.engine.GetGraph()

	// Scope to namespace if provided.
	var nodeIDs []string
	if ns != "" {
		for _, n := range g.AllActiveNodes() {
			if matchNamespace(n.Namespace, ns) {
				nodeIDs = append(nodeIDs, n.ID)
			}
		}
	}

	projectRoot := filepath.Dir(s.engine.MarmotDir)

	opts := curator.AnalyzeOpts{
		NodeIDs:     nodeIDs,
		CheckStale:  checkStale,
		ProjectRoot: projectRoot,
		Limit:       limit,
		Offset:      offset,
	}

	suggestions := curator.Analyze(g, s.engine.NodeStore, s.engine.EmbeddingStore, s.engine.Embedder, opts)

	// Report the number of nodes the analysis actually covered: active nodes
	// only (superseded nodes are never analyzed), scoped to the namespace
	// filter when one was given. Using the raw graph size here inflated the
	// "N nodes · X% curated" health summary with superseded nodes.
	nodeCount := 0
	if g != nil {
		if ns != "" {
			nodeCount = len(nodeIDs)
		} else {
			nodeCount = len(g.AllActiveNodes())
		}
	}

	integrity, integrityNodes := s.collectIntegrityIssues(ns)

	writeJSON(w, http.StatusOK, SuggestionsResponse{
		Suggestions:        suggestions,
		NodeCount:          nodeCount,
		IntegrityIssues:    integrity,
		IntegrityNodeCount: integrityNodes,
	})
}

// collectIntegrityIssues runs the same scoped integrity checks as the /verify
// slash command (superseded nodes included) so the Issues tab shows exactly
// the issues the /verify chat message counts. A non-empty ns restricts the
// checks to that namespace's nodes.
func (s *Server) collectIntegrityIssues(ns string) ([]IntegrityIssueAPI, int) {
	var selected []string
	if ns != "" {
		g := s.engine.GetGraph()
		if g == nil {
			return []IntegrityIssueAPI{}, 0
		}
		for _, n := range g.AllNodes() {
			if matchNamespace(n.Namespace, ns) {
				selected = append(selected, n.ID)
			}
		}
		// An empty selection would make CollectIntegrityIssues cover the
		// whole graph — an unknown namespace must report nothing instead.
		if len(selected) == 0 {
			return []IntegrityIssueAPI{}, 0
		}
	}

	issues, nodes := curator.CollectIntegrityIssues(s.engine, selected)
	out := make([]IntegrityIssueAPI, 0, len(issues))
	for _, issue := range issues {
		out = append(out, IntegrityIssueAPI{
			NodeID:   issue.NodeID,
			Type:     string(issue.IssueType),
			Message:  issue.Message,
			Severity: string(issue.Severity),
		})
	}
	return out, len(nodes)
}

// warrenAuthorReadOnly reports whether the warren manifest marks the mount's
// project readonly (the author veto). Best-effort for error-copy selection
// only — an unreadable manifest returns false; enforcement stays with
// warren.WriteEditableNode's fail-closed backstop.
func warrenAuthorReadOnly(mount warren.ProjectStatus) bool {
	if mount.WarrenPath == "" {
		return false
	}
	manifest, _, err := warren.LoadManifest(mount.WarrenPath)
	if err != nil {
		return false
	}
	for _, p := range manifest.Projects {
		if p.ProjectID == mount.ProjectID && p.ReadOnly {
			return true
		}
	}
	return false
}
