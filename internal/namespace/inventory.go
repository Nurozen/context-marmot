package namespace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nurozen/context-marmot/internal/node"
)

// InventoryItem describes one namespace discovered from manifests, node
// frontmatter, or both.
type InventoryItem struct {
	Name        string `json:"name"`
	NodeCount   int    `json:"node_count"`
	HasManifest bool   `json:"has_manifest"`
	RootPath    string `json:"root_path,omitempty"`
}

// DoctorIssue describes a namespace metadata consistency problem.
type DoctorIssue struct {
	Severity  string `json:"severity"`
	Namespace string `json:"namespace,omitempty"`
	Message   string `json:"message"`
}

// Inventory returns namespaces known from _namespace.md manifests plus
// implicit namespaces found in node frontmatter.
func Inventory(vaultDir string) ([]InventoryItem, error) {
	counts, err := namespaceNodeCounts(vaultDir)
	if err != nil {
		return nil, err
	}

	items := make(map[string]InventoryItem)
	for name, count := range counts {
		items[name] = InventoryItem{Name: name, NodeCount: count}
	}

	mgr, err := NewManager(vaultDir)
	if err != nil {
		return nil, err
	}
	for name, ns := range mgr.Namespaces {
		item := items[name]
		item.Name = name
		item.HasManifest = true
		item.RootPath = ns.RootPath
		items[name] = item
	}

	if len(items) == 0 {
		items["default"] = InventoryItem{Name: "default"}
	}

	ordered := make([]InventoryItem, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})
	return ordered, nil
}

// Doctor validates namespace manifests against node frontmatter and bridge
// endpoints. Missing manifests are warnings because implicit namespaces remain
// readable, but they are worth normalizing.
func Doctor(vaultDir string) ([]DoctorIssue, error) {
	items, err := Inventory(vaultDir)
	if err != nil {
		return nil, err
	}

	var issues []DoctorIssue
	known := make(map[string]struct{})
	for _, item := range items {
		known[item.Name] = struct{}{}
		if item.Name != "default" && item.NodeCount > 0 && !item.HasManifest {
			issues = append(issues, DoctorIssue{
				Severity:  "warning",
				Namespace: item.Name,
				Message:   fmt.Sprintf("namespace %q has %d node(s) but no _namespace.md manifest", item.Name, item.NodeCount),
			})
		}
	}

	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		return nil, fmt.Errorf("read vault dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if dirName == "" || dirName[0] == '.' || dirName[0] == '_' {
			continue
		}
		nsDir := filepath.Join(vaultDir, dirName)
		if _, err := os.Stat(filepath.Join(nsDir, "_namespace.md")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat namespace manifest: %w", err)
		}
		ns, err := LoadNamespace(nsDir)
		if err != nil {
			issues = append(issues, DoctorIssue{
				Severity:  "error",
				Namespace: dirName,
				Message:   fmt.Sprintf("cannot load _namespace.md: %v", err),
			})
			continue
		}
		if ns.Name != dirName {
			issues = append(issues, DoctorIssue{
				Severity:  "error",
				Namespace: dirName,
				Message:   fmt.Sprintf("manifest name %q does not match directory %q", ns.Name, dirName),
			})
		}
	}

	mgr, err := NewManager(vaultDir)
	if err != nil {
		return nil, err
	}
	for _, bridge := range mgr.Bridges {
		if _, ok := known[bridge.Source]; !ok {
			issues = append(issues, DoctorIssue{
				Severity:  "error",
				Namespace: bridge.Source,
				Message:   fmt.Sprintf("bridge references unknown source namespace %q", bridge.Source),
			})
		}
		if _, ok := known[bridge.Target]; !ok {
			issues = append(issues, DoctorIssue{
				Severity:  "error",
				Namespace: bridge.Target,
				Message:   fmt.Sprintf("bridge references unknown target namespace %q", bridge.Target),
			})
		}
	}

	return issues, nil
}

func namespaceNodeCounts(vaultDir string) (map[string]int, error) {
	store := node.NewStore(vaultDir)
	metas, err := store.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	counts := make(map[string]int)
	for _, meta := range metas {
		ns := meta.Namespace
		if ns == "" {
			ns = "default"
		}
		counts[ns]++
	}
	return counts, nil
}
