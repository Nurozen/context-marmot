package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/summary"
	"github.com/nurozen/context-marmot/internal/verify"
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
		allNodes = s.engine.Graph.AllNodes()
	} else {
		allNodes = s.engine.Graph.AllActiveNodes()
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

	for _, n := range filtered {
		outEdges := s.engine.Graph.GetEdges(n.ID, graph.Outbound)
		inEdges := s.engine.Graph.GetEdges(n.ID, graph.Inbound)

		apiNode := nodeToAPI(n, len(outEdges)+len(inEdges))

		if checkStale && n.Source.Path != "" {
			status, err := verify.VerifyStaleness(n, projectRoot)
			if err == nil && status.IsStale {
				apiNode.IsStale = true
			}
		}

		resp.Nodes = append(resp.Nodes, apiNode)

		// Collect flat outbound edges for the response.
		for _, e := range outEdges {
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
		nodeIDs := make([]string, len(filtered))
		for i, n := range filtered {
			nodeIDs[i] = n.ID
		}
		s.engine.HeatMap.RecordCoAccess(nil, 0) // no-op, just to ensure lock safety
		// Gather all pairs that involve nodes in this namespace.
		for _, p := range s.engine.HeatMap.Pairs {
			inNS := false
			for _, id := range nodeIDs {
				if p.A == id || p.B == id {
					inNS = true
					break
				}
			}
			if inNS {
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

	outEdges := s.engine.Graph.GetEdges(n.ID, graph.Outbound)
	inEdges := s.engine.Graph.GetEdges(n.ID, graph.Inbound)
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

	if req.Summary == "" && req.Context == "" {
		writeError(w, http.StatusBadRequest, "at least one of summary or context must be provided")
		return
	}

	n, ok := s.engine.ResolveNodeID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "node not found: "+id)
		return
	}

	// Load from disk to get the full node (including body sections).
	path := s.engine.NodeStore.NodePath(n.ID)
	diskNode, err := s.engine.NodeStore.LoadNode(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load node: "+err.Error())
		return
	}

	summaryChanged := false
	if req.Summary != "" {
		diskNode.Summary = req.Summary
		summaryChanged = true
	}
	if req.Context != "" {
		diskNode.Context = req.Context
	}

	// Persist to disk.
	if err := s.engine.NodeStore.SaveNode(diskNode); err != nil {
		writeError(w, http.StatusInternalServerError, "save node: "+err.Error())
		return
	}

	// Update in-memory graph.
	if err := s.engine.Graph.UpsertNode(diskNode); err != nil {
		writeError(w, http.StatusInternalServerError, "graph upsert: "+err.Error())
		return
	}

	// Re-embed if summary changed.
	if summaryChanged && s.engine.Embedder != nil {
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
		writeJSON(w, http.StatusOK, SearchResponse{Results: []SearchResult{}})
		return
	}

	resp := SearchResponse{Results: make([]SearchResult, 0, len(results))}
	for _, sr := range results {
		n, ok := s.engine.Graph.GetNode(sr.NodeID)
		if !ok {
			continue
		}
		// Apply optional namespace filter.
		if ns != "" && !matchNamespace(n.Namespace, ns) {
			continue
		}
		resp.Results = append(resp.Results, SearchResult{
			NodeID:    sr.NodeID,
			Score:     sr.Score,
			Summary:   n.Summary,
			Type:      n.Type,
			Namespace: n.Namespace,
		})
	}

	writeJSON(w, http.StatusOK, resp)
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
	allNodes := s.engine.Graph.AllActiveNodes()
	nsIDs := make(map[string]bool)
	for _, n := range allNodes {
		if matchNamespace(n.Namespace, namespace) {
			nsIDs[n.ID] = true
		}
	}

	var pairs []APIHeatPair
	for _, p := range s.engine.HeatMap.Pairs {
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

	if s.engine.NSManager == nil {
		// Single-namespace mode: count all active nodes under "default".
		count := len(s.engine.Graph.AllActiveNodes())
		hasSummary := false
		if _, err := summary.ReadSummary(s.engine.MarmotDir, "default"); err == nil {
			hasSummary = true
		}
		resp.Namespaces = append(resp.Namespaces, NamespaceInfo{
			Name:       "default",
			NodeCount:  count,
			HasSummary: hasSummary,
		})
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Count nodes per namespace from the graph.
	nsCounts := make(map[string]int)
	for _, n := range s.engine.Graph.AllActiveNodes() {
		ns := n.Namespace
		if ns == "" {
			ns = "default"
		}
		nsCounts[ns]++
	}

	for name := range s.engine.NSManager.Namespaces {
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

	if s.engine.NSManager == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	for _, b := range s.engine.NSManager.Bridges {
		resp.Bridges = append(resp.Bridges, BridgeInfo{
			Source:           b.Source,
			Target:           b.Target,
			AllowedRelations: b.AllowedRelations,
			IsCrossVault:     b.IsCrossVault(),
		})
	}

	for _, b := range s.engine.NSManager.CrossVaultBridges {
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
	// Also match if the node ID is prefixed with the namespace (e.g., namespace
	// stored in the node ID itself for multi-namespace vaults).
	return strings.HasPrefix(nodeNS, requested)
}

// nodeToAPI converts a domain node to its API representation.
func nodeToAPI(n *node.Node, edgeCount int) APINode {
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
