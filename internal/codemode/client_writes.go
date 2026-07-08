package codemode

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/graph"
)

// registerWrites adds tag/untag/type/link/unlink/merge/delete/verify methods
// to the `client` global. Each method:
//
//  1. Verifies the executor was given a WriteContext (otherwise throws).
//  2. Verifies the engine is writable (otherwise throws "vault is read-only").
//  3. Enforces the per-turn mutation cap (otherwise throws).
//  4. Snapshots the affected nodes, dispatches through curator.ExecuteCommand
//     using the same SlashCommand pipeline the chat UI uses, pushes an undo
//     entry, and records a MutationRecord on the scope.
//  5. Returns a small status object to JS:
//     { applied: bool, message: string, affected: string[], undo_id: string }
//
// On any failure path the method appends a record with Success=false so the
// audit trail surfaces attempted-but-rejected mutations to the user.
func registerWrites(rt *goja.Runtime, client *goja.Object, scope *runScope) error {
	if scope.write == nil {
		// No write context — leave write methods unregistered. The phase-1
		// prompt advertises them as conditional, so the LLM will adapt.
		return nil
	}
	if scope.write.ReadOnly {
		// Read-only vault — register a single stub that throws so the LLM
		// gets a clear error rather than silently failing.
		throwReadOnly := func(name string) func(goja.FunctionCall) goja.Value {
			return func(call goja.FunctionCall) goja.Value {
				panic(rt.NewGoError(fmt.Errorf("client.%s: vault is read-only; writes are disabled", name)))
			}
		}
		for _, name := range []string{"tag", "untag", "setType", "link", "unlink", "merge", "delete"} {
			if err := client.Set(name, throwReadOnly(name)); err != nil {
				return err
			}
		}
		return nil
	}

	mustSet := func(name string, fn any) {
		if err := client.Set(name, fn); err != nil {
			panic(rt.NewGoError(fmt.Errorf("register write %s: %w", name, err)))
		}
	}

	// tag(idOrIds, tagOrTags) — add tag(s) to node(s).
	mustSet("tag", func(call goja.FunctionCall) goja.Value {
		ids := stringSliceArg(rt, call.Argument(0), "client.tag: first argument must be id or [ids]")
		tags := stringSliceArg(rt, call.Argument(1), "client.tag: second argument must be tag or [tags]")
		return rt.ToValue(scope.runWrite("tag", ids, func(cmd *curator.SlashCommand) {
			cmd.Args = tags
		}))
	})

	// untag(idOrIds, tagOrTags) — remove tag(s) from node(s).
	mustSet("untag", func(call goja.FunctionCall) goja.Value {
		ids := stringSliceArg(rt, call.Argument(0), "client.untag: first argument must be id or [ids]")
		tags := stringSliceArg(rt, call.Argument(1), "client.untag: second argument must be tag or [tags]")
		return rt.ToValue(scope.runWrite("untag", ids, func(cmd *curator.SlashCommand) {
			cmd.Args = tags
		}))
	})

	// setType(idOrIds, newType) — change the type field of node(s).
	mustSet("setType", func(call goja.FunctionCall) goja.Value {
		ids := stringSliceArg(rt, call.Argument(0), "client.setType: first argument must be id or [ids]")
		newType := call.Argument(1).String()
		if newType == "" {
			panic(rt.NewTypeError("client.setType: type argument is required"))
		}
		return rt.ToValue(scope.runWrite("type", ids, func(cmd *curator.SlashCommand) {
			cmd.Args = []string{newType}
		}))
	})

	// link(source, relation, target) — create a new edge.
	mustSet("link", func(call goja.FunctionCall) goja.Value {
		src := call.Argument(0).String()
		rel := call.Argument(1).String()
		tgt := call.Argument(2).String()
		if src == "" || rel == "" || tgt == "" {
			panic(rt.NewTypeError("client.link: source, relation, target are all required"))
		}
		return rt.ToValue(scope.runWrite("link", []string{src, tgt}, func(cmd *curator.SlashCommand) {
			cmd.Args = []string{src, rel, tgt}
		}))
	})

	// unlink(source, relation, target) — remove an edge.
	mustSet("unlink", func(call goja.FunctionCall) goja.Value {
		src := call.Argument(0).String()
		rel := call.Argument(1).String()
		tgt := call.Argument(2).String()
		if src == "" || rel == "" || tgt == "" {
			panic(rt.NewTypeError("client.unlink: source, relation, target are all required"))
		}
		return rt.ToValue(scope.runWrite("unlink", []string{src, tgt}, func(cmd *curator.SlashCommand) {
			cmd.Args = []string{src, rel, tgt}
		}))
	})

	// merge(into, from) — soft-delete `from` and redirect its edges to `into`.
	mustSet("merge", func(call goja.FunctionCall) goja.Value {
		into := call.Argument(0).String()
		from := call.Argument(1).String()
		if into == "" || from == "" {
			panic(rt.NewTypeError("client.merge: both ids are required (into, from)"))
		}
		return rt.ToValue(scope.runWrite("merge", []string{into, from}, func(cmd *curator.SlashCommand) {
			cmd.Args = []string{into, from}
		}))
	})

	// delete(idOrIds) — soft-delete node(s).
	mustSet("delete", func(call goja.FunctionCall) goja.Value {
		ids := stringSliceArg(rt, call.Argument(0), "client.delete: argument must be id or [ids]")
		return rt.ToValue(scope.runWrite("delete", ids, func(cmd *curator.SlashCommand) {}))
	})

	// verify() — read-only diagnostic. Doesn't mutate but goes through the
	// command path for consistency. Doesn't count against the mutation cap.
	mustSet("verify", func(call goja.FunctionCall) goja.Value {
		cmd := &curator.SlashCommand{Name: "verify"}
		result, err := curator.ExecuteCommand(scope.ctxOrBackground(), cmd, scope.engine, scope.write.SelectedNodes)
		if err != nil {
			panic(rt.NewGoError(err))
		}
		return rt.ToValue(map[string]any{
			"applied": result.Success,
			"message": result.Message,
		})
	})

	return nil
}

// runWrite is the shared implementation for the tag/untag/type/link/unlink/
// merge/delete write methods. It enforces the cap, snapshots, dispatches,
// records, and returns the JS-facing status object.
func (s *runScope) runWrite(op string, affectedIDs []string, configure func(*curator.SlashCommand)) map[string]any {
	// Mutation cap.
	if len(s.mutations) >= s.mutationsCap {
		s.recordFailure(op, affectedIDs, fmt.Sprintf("mutation cap reached (%d per turn)", s.mutationsCap))
		panic(s.rt.NewGoError(fmt.Errorf("client.%s: mutation cap of %d per turn reached; remaining writes were dropped", op, s.mutationsCap)))
	}

	// Resolve affected IDs for the snapshot. For commands that operate on
	// "selected" nodes (tag, untag, type, delete), the ExecuteCommand layer
	// reads selectedNodes; we pass the explicit IDs in via the same slot so
	// code-mode doesn't depend on UI selection state.
	cmd := &curator.SlashCommand{Name: op}
	configure(cmd)
	affectedIDs = s.canonicalizeCommandIDs(op, affectedIDs, cmd)

	// Snapshot every node the mutation might modify. For /merge, the
	// underlying handler rewrites edges on third-party nodes that point at
	// `from` so they instead point at `into`; if we don't snapshot those
	// sources, undo silently corrupts the graph. Compute the full snapshot
	// set up front.
	snapshotIDs := affectedIDs
	if op == "merge" && len(affectedIDs) >= 2 {
		fromID := affectedIDs[1] // configure() sets Args[1] = from
		extra := inboundSourceIDs(s.engine.GetGraph(), fromID)
		snapshotIDs = mergeIDLists(affectedIDs, extra)
	}

	unlock := s.lockNamespaces(snapshotIDs)
	defer unlock()

	snapshots := curator.SnapshotNodes(s.engine.NodeStore, snapshotIDs)

	result, err := curator.ExecuteCommand(s.ctxOrBackground(), cmd, s.engine, affectedIDs)
	if err != nil {
		s.recordFailure(op, affectedIDs, err.Error())
		return map[string]any{
			"applied":  false,
			"message":  err.Error(),
			"affected": []string{},
			"undo_id":  "",
		}
	}

	if !result.Success {
		s.recordFailure(op, affectedIDs, result.Message)
		return map[string]any{
			"applied":  false,
			"message":  result.Message,
			"affected": []string{},
			"undo_id":  "",
		}
	}

	// Soft failure: the underlying command claimed success but no node was
	// actually mutated (e.g. /tag on a missing ID). Treat as a failure so
	// the audit trail surfaces it and no undo entry is pushed for a no-op.
	if len(result.MutatedNodes) == 0 && len(affectedIDs) > 0 && op != "verify" {
		msg := result.Message
		if !strings.Contains(strings.ToLower(msg), "no node") {
			msg = msg + " (no matching nodes)"
		}
		s.recordFailure(op, affectedIDs, msg)
		return map[string]any{
			"applied":  false,
			"message":  msg,
			"affected": []string{},
			"undo_id":  "",
		}
	}

	// Push an undo entry so the user can roll this back. Mirrors the slash
	// command path in api/chat_handlers.go.
	undoID := fmt.Sprintf("undo-%d", time.Now().UnixNano())
	entry := curator.UndoEntry{
		ID:        undoID,
		SessionID: s.write.SessionID,
		Timestamp: time.Now(),
		Snapshots: snapshots,
	}
	s.write.UndoStack.Push(s.write.SessionID, entry)

	if s.write.NotifyChange != nil {
		s.write.NotifyChange()
	}

	rec := curator.MutationRecord{
		Op:      op,
		Message: result.Message,
		Nodes:   normalizeAffected(result.MutatedNodes, affectedIDs),
		UndoID:  undoID,
		Success: true,
	}
	s.mutations = append(s.mutations, rec)

	return map[string]any{
		"applied":  true,
		"message":  result.Message,
		"affected": rec.Nodes,
		"undo_id":  undoID,
	}
}

func (s *runScope) canonicalizeCommandIDs(op string, affectedIDs []string, cmd *curator.SlashCommand) []string {
	canonical := func(id string) string {
		if n, ok := s.engine.ResolveNodeID(id); ok {
			return n.ID
		}
		return id
	}
	out := make([]string, len(affectedIDs))
	for i, id := range affectedIDs {
		out[i] = canonical(id)
	}
	switch op {
	case "link", "unlink":
		if len(cmd.Args) >= 3 {
			cmd.Args[0] = canonical(cmd.Args[0])
			cmd.Args[2] = canonical(cmd.Args[2])
			out = []string{cmd.Args[0], cmd.Args[2]}
		}
	case "merge":
		if len(cmd.Args) >= 2 {
			cmd.Args[0] = canonical(cmd.Args[0])
			cmd.Args[1] = canonical(cmd.Args[1])
			out = []string{cmd.Args[0], cmd.Args[1]}
		}
	}
	return out
}

func (s *runScope) lockNamespaces(ids []string) func() {
	if s.engine == nil {
		return func() {}
	}
	namespaces := make(map[string]struct{})
	for _, id := range ids {
		ns := s.write.Namespace
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

// recordFailure appends a Success=false MutationRecord so the audit panel
// shows attempted-but-rejected writes.
func (s *runScope) recordFailure(op string, affectedIDs []string, msg string) {
	s.mutations = append(s.mutations, curator.MutationRecord{
		Op:      op,
		Message: msg,
		Nodes:   normalizeAffected(nil, affectedIDs),
		Success: false,
	})
}

func normalizeAffected(fromResult, fallback []string) []string {
	if len(fromResult) > 0 {
		return fromResult
	}
	return fallback
}

// ctxOrBackground returns the scope's context if non-nil, otherwise a
// fresh context.Background(). Tests build runScope directly without
// going through Execute so the ctx field may be unset.
func (s *runScope) ctxOrBackground() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// inboundSourceIDs returns the IDs of nodes that have an outbound edge
// pointing at targetID. Used by /merge's snapshot path so undo can restore
// every node whose edges get rewritten by the merge.
func inboundSourceIDs(g *graph.Graph, targetID string) []string {
	if g == nil {
		return nil
	}
	edges := g.GetEdges(targetID, graph.Inbound)
	if len(edges) == 0 {
		return nil
	}
	out := make([]string, 0, len(edges))
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if _, ok := seen[e.Target]; ok {
			continue
		}
		seen[e.Target] = struct{}{}
		out = append(out, e.Target)
	}
	return out
}

// mergeIDLists returns the union of two ID slices, preserving order from
// the first slice and appending unique entries from the second.
func mergeIDLists(primary, extra []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(extra))
	out := make([]string, 0, len(primary)+len(extra))
	for _, id := range primary {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range extra {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// stringSliceArg coerces a JS argument into a []string. Accepts a single
// string or a JS array of strings. Throws on anything else.
func stringSliceArg(rt *goja.Runtime, v goja.Value, errMsg string) []string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		panic(rt.NewTypeError(errMsg))
	}
	exp := v.Export()
	switch t := exp.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			panic(rt.NewTypeError(errMsg))
		}
		return []string{s}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			panic(rt.NewTypeError(errMsg))
		}
		return out
	}
	panic(rt.NewTypeError(errMsg))
}
