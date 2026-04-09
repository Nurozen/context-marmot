// Package curator provides slash command parsing and execution for
// ContextMarmot's graph curation interface.
package curator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/verify"
)

// SlashCommand represents a parsed slash command from the chat interface.
type SlashCommand struct {
	Name string   // "tag", "untag", "type", "merge", "delete", "link", "unlink", "verify"
	Args []string // positional arguments
}

// CommandResult is the JSON response returned after executing a slash command.
type CommandResult struct {
	Success      bool     `json:"success"`
	Message      string   `json:"message"`
	MutatedNodes []string `json:"mutated_nodes,omitempty"`
}

// validCommands is the set of recognised slash command names.
var validCommands = map[string]bool{
	"tag":    true,
	"untag":  true,
	"type":   true,
	"merge":  true,
	"delete": true,
	"link":   true,
	"unlink": true,
	"verify": true,
}

// allowedTypes is the set of valid node types for /type.
var allowedTypes = map[string]bool{
	"function":  true,
	"module":    true,
	"class":     true,
	"concept":   true,
	"decision":  true,
	"reference": true,
	"composite": true,
}

// validRelations is the set of valid EdgeRelation values for /link and /unlink.
var validRelations = map[node.EdgeRelation]bool{
	node.Contains:     true,
	node.Imports:      true,
	node.Extends:      true,
	node.Implements:   true,
	node.Calls:        true,
	node.Reads:        true,
	node.Writes:       true,
	node.References:   true,
	node.CrossProject: true,
	node.Associated:   true,
}

// ParseCommand parses a chat message into a SlashCommand. Returns nil, false
// if the message does not start with '/'. Returns a command with the parsed
// name and args otherwise. The bool indicates whether a slash command was
// detected (even if the command name is invalid).
func ParseCommand(msg string) (*SlashCommand, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" || msg[0] != '/' {
		return nil, false
	}

	tokens := tokenize(msg)
	if len(tokens) == 0 {
		return nil, false
	}

	name := strings.TrimPrefix(tokens[0], "/")
	if name == "" {
		return nil, false
	}

	return &SlashCommand{
		Name: name,
		Args: tokens[1:],
	}, true
}

// tokenize splits a message into space-separated tokens, supporting double-quoted
// strings that may contain spaces. Quotes are stripped from the result.
func tokenize(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// ExecuteCommand dispatches a parsed slash command against the engine.
// selectedNodes are the IDs of nodes currently selected in the UI.
func ExecuteCommand(ctx context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	if !validCommands[cmd.Name] {
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("unknown command: /%s", cmd.Name),
		}, nil
	}

	switch cmd.Name {
	case "tag":
		return executeTag(ctx, cmd, engine, selectedNodes)
	case "untag":
		return executeUntag(ctx, cmd, engine, selectedNodes)
	case "type":
		return executeType(ctx, cmd, engine, selectedNodes)
	case "merge":
		return executeMerge(ctx, cmd, engine)
	case "delete":
		return executeDelete(ctx, cmd, engine, selectedNodes)
	case "link":
		return executeLink(ctx, cmd, engine)
	case "unlink":
		return executeUnlink(ctx, cmd, engine)
	case "verify":
		return executeVerify(ctx, cmd, engine, selectedNodes)
	default:
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("unknown command: /%s", cmd.Name),
		}, nil
	}
}

// executeTag adds a tag to each selected node.
func executeTag(_ context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	if len(cmd.Args) < 1 {
		return &CommandResult{Success: false, Message: "/tag requires a tag name"}, nil
	}
	tag := cmd.Args[0]
	if len(selectedNodes) == 0 {
		return &CommandResult{Success: false, Message: "/tag requires at least one selected node"}, nil
	}

	var mutated []string
	for _, id := range selectedNodes {
		n, ok := engine.ResolveNodeID(id)
		if !ok {
			continue
		}
		diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(n.ID))
		if err != nil {
			continue
		}
		// Skip if tag already present.
		hasTag := false
		for _, t := range diskNode.Tags {
			if t == tag {
				hasTag = true
				break
			}
		}
		if hasTag {
			mutated = append(mutated, diskNode.ID)
			continue
		}
		diskNode.Tags = append(diskNode.Tags, tag)
		if err := engine.NodeStore.SaveNode(diskNode); err != nil {
			continue
		}
		_ = engine.GetGraph().UpsertNode(diskNode)
		mutated = append(mutated, diskNode.ID)
	}

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("tagged %d node(s) with %q", len(mutated), tag),
		MutatedNodes: mutated,
	}, nil
}

// executeUntag removes a tag from each selected node.
func executeUntag(_ context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	if len(cmd.Args) < 1 {
		return &CommandResult{Success: false, Message: "/untag requires a tag name"}, nil
	}
	tag := cmd.Args[0]
	if len(selectedNodes) == 0 {
		return &CommandResult{Success: false, Message: "/untag requires at least one selected node"}, nil
	}

	var mutated []string
	for _, id := range selectedNodes {
		n, ok := engine.ResolveNodeID(id)
		if !ok {
			continue
		}
		diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(n.ID))
		if err != nil {
			continue
		}
		newTags := make([]string, 0, len(diskNode.Tags))
		removed := false
		for _, t := range diskNode.Tags {
			if t == tag && !removed {
				removed = true
				continue
			}
			newTags = append(newTags, t)
		}
		if !removed {
			continue
		}
		diskNode.Tags = newTags
		if err := engine.NodeStore.SaveNode(diskNode); err != nil {
			continue
		}
		_ = engine.GetGraph().UpsertNode(diskNode)
		mutated = append(mutated, diskNode.ID)
	}

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("removed tag %q from %d node(s)", tag, len(mutated)),
		MutatedNodes: mutated,
	}, nil
}

// executeType changes the type of selected nodes.
func executeType(_ context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	if len(cmd.Args) < 1 {
		return &CommandResult{Success: false, Message: "/type requires a type name"}, nil
	}
	newType := cmd.Args[0]
	if !allowedTypes[newType] {
		allowed := make([]string, 0, len(allowedTypes))
		for t := range allowedTypes {
			allowed = append(allowed, t)
		}
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("invalid type %q; allowed: %s", newType, strings.Join(allowed, ", ")),
		}, nil
	}
	if len(selectedNodes) == 0 {
		return &CommandResult{Success: false, Message: "/type requires at least one selected node"}, nil
	}

	var mutated []string
	for _, id := range selectedNodes {
		n, ok := engine.ResolveNodeID(id)
		if !ok {
			continue
		}
		diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(n.ID))
		if err != nil {
			continue
		}
		diskNode.Type = newType
		if err := engine.NodeStore.SaveNode(diskNode); err != nil {
			continue
		}
		_ = engine.GetGraph().UpsertNode(diskNode)
		mutated = append(mutated, diskNode.ID)
	}

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("changed type to %q for %d node(s)", newType, len(mutated)),
		MutatedNodes: mutated,
	}, nil
}

// executeMerge merges node B into node A: unions B's edges into A (avoiding
// duplicates), copies B's tags that A doesn't have, then deletes B.
func executeMerge(_ context.Context, cmd *SlashCommand, engine *mcp.Engine) (*CommandResult, error) {
	if len(cmd.Args) < 2 {
		return &CommandResult{Success: false, Message: "/merge requires two node IDs: /merge <A> <B>"}, nil
	}
	idA, idB := cmd.Args[0], cmd.Args[1]

	nodeA, okA := engine.ResolveNodeID(idA)
	if !okA {
		return &CommandResult{Success: false, Message: fmt.Sprintf("node %q not found", idA)}, nil
	}
	nodeB, okB := engine.ResolveNodeID(idB)
	if !okB {
		return &CommandResult{Success: false, Message: fmt.Sprintf("node %q not found", idB)}, nil
	}

	// Load both from disk to get full content.
	diskA, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(nodeA.ID))
	if err != nil {
		return nil, fmt.Errorf("load node A: %w", err)
	}
	diskB, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(nodeB.ID))
	if err != nil {
		return nil, fmt.Errorf("load node B: %w", err)
	}

	// Union B's edges into A, avoiding duplicates.
	existingEdges := make(map[string]bool)
	for _, e := range diskA.Edges {
		key := string(e.Relation) + "|" + e.Target
		existingEdges[key] = true
	}
	for _, e := range diskB.Edges {
		key := string(e.Relation) + "|" + e.Target
		// Skip edges that point back to A (self-loop after merge) or already exist.
		if e.Target == diskA.ID || existingEdges[key] {
			continue
		}
		diskA.Edges = append(diskA.Edges, e)
		existingEdges[key] = true
	}

	// Copy B's tags that A doesn't have.
	existingTags := make(map[string]bool)
	for _, t := range diskA.Tags {
		existingTags[t] = true
	}
	for _, t := range diskB.Tags {
		if !existingTags[t] {
			diskA.Tags = append(diskA.Tags, t)
			existingTags[t] = true
		}
	}

	// Save A with merged content.
	if err := engine.NodeStore.SaveNode(diskA); err != nil {
		return nil, fmt.Errorf("save merged node A: %w", err)
	}
	_ = engine.GetGraph().UpsertNode(diskA)

	// Delete B (soft-delete pointing to A as superseder).
	if err := engine.NodeStore.SoftDeleteNode(diskB.ID, diskA.ID); err != nil {
		return nil, fmt.Errorf("soft-delete node B: %w", err)
	}
	// Reload B and update graph.
	reloaded, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(diskB.ID))
	if err == nil {
		_ = engine.GetGraph().UpsertNode(reloaded)
	}

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("merged %q into %q", diskB.ID, diskA.ID),
		MutatedNodes: []string{diskA.ID, diskB.ID},
	}, nil
}

// executeDelete soft-deletes selected nodes.
func executeDelete(_ context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	if len(selectedNodes) == 0 {
		return &CommandResult{Success: false, Message: "/delete requires at least one selected node"}, nil
	}

	var mutated []string
	for _, id := range selectedNodes {
		n, ok := engine.ResolveNodeID(id)
		if !ok {
			continue
		}
		if err := engine.NodeStore.SoftDeleteNode(n.ID, ""); err != nil {
			continue
		}
		// Reload and update in-memory graph.
		reloaded, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(n.ID))
		if err == nil {
			_ = engine.GetGraph().UpsertNode(reloaded)
		}
		mutated = append(mutated, n.ID)
	}

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("deleted %d node(s)", len(mutated)),
		MutatedNodes: mutated,
	}, nil
}

// executeLink adds an edge from source to target with the given relation.
func executeLink(_ context.Context, cmd *SlashCommand, engine *mcp.Engine) (*CommandResult, error) {
	if len(cmd.Args) < 3 {
		return &CommandResult{Success: false, Message: "/link requires: /link <source> <relation> <target>"}, nil
	}
	sourceID, relation, targetID := cmd.Args[0], cmd.Args[1], cmd.Args[2]

	rel := node.EdgeRelation(relation)
	if !validRelations[rel] {
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("invalid relation %q", relation),
		}, nil
	}

	srcNode, ok := engine.ResolveNodeID(sourceID)
	if !ok {
		return &CommandResult{Success: false, Message: fmt.Sprintf("source node %q not found", sourceID)}, nil
	}
	// Verify target exists.
	tgtNode, ok := engine.ResolveNodeID(targetID)
	if !ok {
		return &CommandResult{Success: false, Message: fmt.Sprintf("target node %q not found", targetID)}, nil
	}

	// Load source from disk, add edge, save.
	diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(srcNode.ID))
	if err != nil {
		return nil, fmt.Errorf("load source node: %w", err)
	}

	newEdge := node.Edge{
		Target:   tgtNode.ID,
		Relation: rel,
		Class:    node.ClassifyRelation(relation),
	}
	diskNode.Edges = append(diskNode.Edges, newEdge)

	if err := engine.NodeStore.SaveNode(diskNode); err != nil {
		return nil, fmt.Errorf("save source node: %w", err)
	}
	_ = engine.GetGraph().UpsertNode(diskNode)

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("linked %s -[%s]-> %s", srcNode.ID, relation, tgtNode.ID),
		MutatedNodes: []string{srcNode.ID},
	}, nil
}

// executeUnlink removes an edge from source to target matching the given relation.
func executeUnlink(_ context.Context, cmd *SlashCommand, engine *mcp.Engine) (*CommandResult, error) {
	if len(cmd.Args) < 3 {
		return &CommandResult{Success: false, Message: "/unlink requires: /unlink <source> <relation> <target>"}, nil
	}
	sourceID, relation, targetID := cmd.Args[0], cmd.Args[1], cmd.Args[2]

	rel := node.EdgeRelation(relation)
	if !validRelations[rel] {
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("invalid relation %q", relation),
		}, nil
	}

	srcNode, ok := engine.ResolveNodeID(sourceID)
	if !ok {
		return &CommandResult{Success: false, Message: fmt.Sprintf("source node %q not found", sourceID)}, nil
	}
	tgtNode, ok := engine.ResolveNodeID(targetID)
	if !ok {
		return &CommandResult{Success: false, Message: fmt.Sprintf("target node %q not found", targetID)}, nil
	}

	// Load source from disk, remove matching edge, save.
	diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(srcNode.ID))
	if err != nil {
		return nil, fmt.Errorf("load source node: %w", err)
	}

	newEdges := make([]node.Edge, 0, len(diskNode.Edges))
	removed := false
	for _, e := range diskNode.Edges {
		if !removed && e.Target == tgtNode.ID && e.Relation == rel {
			removed = true
			continue
		}
		newEdges = append(newEdges, e)
	}
	if !removed {
		return &CommandResult{
			Success: false,
			Message: fmt.Sprintf("no edge %s -[%s]-> %s found", srcNode.ID, relation, tgtNode.ID),
		}, nil
	}

	diskNode.Edges = newEdges
	if err := engine.NodeStore.SaveNode(diskNode); err != nil {
		return nil, fmt.Errorf("save source node: %w", err)
	}
	_ = engine.GetGraph().UpsertNode(diskNode)

	return &CommandResult{
		Success:      true,
		Message:      fmt.Sprintf("unlinked %s -[%s]-> %s", srcNode.ID, relation, tgtNode.ID),
		MutatedNodes: []string{srcNode.ID},
	}, nil
}

// executeVerify runs integrity/staleness checks on selected nodes (or all nodes).
func executeVerify(_ context.Context, _ *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
	g := engine.GetGraph()

	var nodes []*node.Node
	if len(selectedNodes) == 0 {
		nodes = g.AllNodes()
	} else {
		for _, id := range selectedNodes {
			if n, ok := engine.ResolveNodeID(id); ok {
				nodes = append(nodes, n)
			}
		}
	}

	if len(nodes) == 0 {
		return &CommandResult{
			Success: true,
			Message: "no nodes to verify",
		}, nil
	}

	// MarmotDir is typically .marmot; project root is its parent.
	projectRoot := filepath.Dir(engine.MarmotDir)

	issues := verify.VerifyIntegrity(nodes, projectRoot)

	if len(issues) == 0 {
		return &CommandResult{
			Success: true,
			Message: fmt.Sprintf("verified %d node(s) with no issues", len(nodes)),
		}, nil
	}

	return &CommandResult{
		Success: true,
		Message: fmt.Sprintf("found %d issue(s) across %d node(s)", len(issues), len(nodes)),
	}, nil
}
