package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
			if p.Weight < 0.06 { continue } // skip pairs at decay floor
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
	if s.engine.NSManager != nil {
		for name := range s.engine.NSManager.Namespaces {
			if !nsSet[name] {
				nsSet[name] = true
				namespaces = append(namespaces, name)
			}
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
			if p.Weight < 0.06 { continue } // skip pairs at decay floor
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

	summaryChanged := false
	if req.Summary != "" {
		diskNode.Summary = req.Summary
		summaryChanged = true
	}
	if req.Context != "" {
		diskNode.Context = req.Context
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
		n, ok := s.engine.GetGraph().GetNode(sr.NodeID)
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
	allNodes := s.engine.GetGraph().AllActiveNodes()
	nsIDs := make(map[string]bool)
	for _, n := range allNodes {
		if matchNamespace(n.Namespace, namespace) {
			nsIDs[n.ID] = true
		}
	}

	var pairs []APIHeatPair
	for _, p := range s.engine.HeatMap.AllPairs() {
		if p.Weight < 0.06 { continue } // skip pairs at decay floor
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
		count := len(s.engine.GetGraph().AllActiveNodes())
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
	for _, n := range s.engine.GetGraph().AllActiveNodes() {
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
	return false
}

// handleVersion returns the current graph version counter.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int64{"version": s.version.Load()})
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
