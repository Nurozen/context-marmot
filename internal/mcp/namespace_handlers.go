package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
)

type NamespaceToolResult struct {
	Action     string                    `json:"action"`
	Namespace  *namespace.InventoryItem  `json:"namespace,omitempty"`
	Namespaces []namespace.InventoryItem `json:"namespaces,omitempty"`
	Issues     []namespace.DoctorIssue   `json:"issues,omitempty"`
	Created    bool                      `json:"created,omitempty"`
	Removed    bool                      `json:"removed,omitempty"`
}

func (e *Engine) HandleContextNamespace(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := req.GetString("action", "list")
	name := req.GetString("name", "")
	rootPath := req.GetString("root_path", "")
	force := req.GetBool("force", false)

	switch action {
	case "list":
		items, err := namespace.Inventory(e.MarmotDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list namespaces: %v", err)), nil
		}
		return mcp.NewToolResultJSON(NamespaceToolResult{Action: action, Namespaces: items})

	case "create":
		if name == "" {
			return mcp.NewToolResultError("name parameter is required"), nil
		}
		ns, created, err := namespace.EnsureNamespace(e.MarmotDir, name, rootPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create namespace: %v", err)), nil
		}
		e.refreshNamespaceManager(ns)
		return mcp.NewToolResultJSON(NamespaceToolResult{
			Action:    action,
			Created:   created,
			Namespace: &namespace.InventoryItem{Name: ns.Name, HasManifest: true, RootPath: ns.RootPath},
		})

	case "update":
		if name == "" {
			return mcp.NewToolResultError("name parameter is required"), nil
		}
		if err := namespace.ValidateNamespaceName(name); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update namespace: %v", err)), nil
		}
		nsDir := filepath.Join(e.MarmotDir, name)
		ns, err := namespace.LoadNamespace(nsDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load namespace: %v", err)), nil
		}
		ns.RootPath = rootPath
		if err := namespace.SaveNamespace(nsDir, ns); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save namespace: %v", err)), nil
		}
		e.refreshNamespaceManager(ns)
		return mcp.NewToolResultJSON(NamespaceToolResult{
			Action:    action,
			Namespace: &namespace.InventoryItem{Name: ns.Name, HasManifest: true, RootPath: ns.RootPath},
		})

	case "doctor":
		issues, err := namespace.Doctor(e.MarmotDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("doctor namespaces: %v", err)), nil
		}
		return mcp.NewToolResultJSON(NamespaceToolResult{Action: action, Issues: issues})

	case "remove":
		if name == "" {
			return mcp.NewToolResultError("name parameter is required"), nil
		}
		if name == "default" {
			return mcp.NewToolResultError("default namespace manifest cannot be removed through MCP"), nil
		}
		if err := namespace.ValidateNamespaceName(name); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remove namespace: %v", err)), nil
		}
		items, err := namespace.Inventory(e.MarmotDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remove namespace: %v", err)), nil
		}
		for _, item := range items {
			if item.Name == name && item.NodeCount > 0 && !force {
				return mcp.NewToolResultError(fmt.Sprintf("namespace %q still has %d node(s); pass force=true to remove only the manifest", name, item.NodeCount)), nil
			}
		}
		manifestPath := filepath.Join(e.MarmotDir, name, "_namespace.md")
		if err := os.Remove(manifestPath); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remove namespace: %v", err)), nil
		}
		e.nsMgrMu.Lock()
		if e.NSManager != nil {
			delete(e.NSManager.Namespaces, name)
		}
		e.nsMgrMu.Unlock()
		return mcp.NewToolResultJSON(NamespaceToolResult{Action: action, Removed: true})

	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown namespace action %q", action)), nil
	}
}

func (e *Engine) refreshNamespaceManager(ns *namespace.Namespace) {
	e.nsMgrMu.Lock()
	defer e.nsMgrMu.Unlock()
	if e.NSManager != nil {
		e.NSManager.Namespaces[ns.Name] = ns
		return
	}
	if mgr, err := namespace.NewManager(e.MarmotDir); err == nil {
		e.NSManager = mgr
	}
}
