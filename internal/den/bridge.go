package den

// Den-scoped bridges (plan §7): consumer-owned cross-vault edge policies stored
// under $MARMOT_HOME/dens/<den-id>/_bridges/ — the den DIR, sibling of _den.md
// and vault/, NOT inside the identity vault. A den bridge reuses the namespace
// cross-vault bridge manifest shape (_bridges/@a--@b.md, source/target vault
// ids + allowed_relations) and is loaded ADDITIONALLY when the den's vault is
// served, so an agent working in a den can declare which cross-vault edges its
// linked vaults may carry without editing any served vault. Exec-free, atomic
// writes (tmp+rename) — same posture as the rest of internal/den.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nurozen/context-marmot/internal/namespace"
	"gopkg.in/yaml.v3"
)

// BridgesDir returns $MARMOT_HOME/dens/<den-id>/_bridges (the den bridge
// directory; may not exist).
func BridgesDir(denID string) string {
	return filepath.Join(Path(denID), "_bridges")
}

// ListBridges returns the den's bridges by den id, skipping malformed manifest
// files (mirrors namespace.Manager.loadBridges).
func ListBridges(denID string) ([]*namespace.Bridge, error) {
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	return loadBridgesFrom(BridgesDir(denID))
}

// ListBridgesAt is ListBridges addressed by den ROOT path rather than den id.
// The MCP den-bridge loader resolves the den root relative to a served vault
// directory and has no den id in hand.
func ListBridgesAt(denRoot string) ([]*namespace.Bridge, error) {
	return loadBridgesFrom(filepath.Join(denRoot, "_bridges"))
}

// loadBridgesFrom reads every *.md bridge manifest under dir, skipping malformed
// files, and returns them sorted by (source, target) for deterministic output.
func loadBridgesFrom(dir string) ([]*namespace.Bridge, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no _bridges directory is fine
		}
		return nil, fmt.Errorf("read den bridges dir: %w", err)
	}
	var bridges []*namespace.Bridge
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		b, lerr := namespace.LoadBridge(filepath.Join(dir, entry.Name()))
		if lerr != nil {
			continue // skip malformed, like namespace.loadBridges
		}
		bridges = append(bridges, b)
	}
	sort.Slice(bridges, func(i, j int) bool {
		if bridges[i].SourceVaultID != bridges[j].SourceVaultID {
			return bridges[i].SourceVaultID < bridges[j].SourceVaultID
		}
		return bridges[i].TargetVaultID < bridges[j].TargetVaultID
	})
	return bridges, nil
}

// AddBridge declares (or extends) a cross-vault edge policy from -> to on the
// den. from/to are vault ids, accepted with or without a leading '@'. The
// returned bool is true only when a NEW bridge manifest was written; a bridge
// that already exists in either orientation is extended in place (relations
// merged as a set) and returns added=false with the merged bridge — an exact
// re-add is an idempotent no-op (added=false, no write).
func AddBridge(denID, from, to string, relations []string) (*namespace.Bridge, bool, error) {
	if err := ValidateDenID(denID); err != nil {
		return nil, false, err
	}
	f, err := normalizeBridgeVaultID(from)
	if err != nil {
		return nil, false, fmt.Errorf("from: %w", err)
	}
	t, err := normalizeBridgeVaultID(to)
	if err != nil {
		return nil, false, fmt.Errorf("to: %w", err)
	}
	if f == t {
		return nil, false, fmt.Errorf("bridge source and target must differ (both %q)", f)
	}
	rels := dedupeRelations(relations)

	dir := BridgesDir(denID)
	if path, existing, ok := existingBridge(dir, f, t); ok {
		merged, changed := mergeRelations(existing.AllowedRelations, rels)
		if !changed {
			return existing, false, nil
		}
		existing.AllowedRelations = merged
		if err := writeBridgeFile(path, existing); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}

	b := &namespace.Bridge{
		Source:           f,
		Target:           t,
		SourceVaultID:    f,
		TargetVaultID:    t,
		AllowedRelations: rels,
		Created:          time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, fmt.Errorf("create den bridges dir: %w", err)
	}
	if err := writeBridgeFile(filepath.Join(dir, bridgeFileName(f, t)), b); err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// RemoveBridge deletes the den bridge between from and to, matching either
// orientation (@a--@b or @b--@a). Returns removed=false (no error) when no such
// bridge exists.
func RemoveBridge(denID, from, to string) (bool, error) {
	if err := ValidateDenID(denID); err != nil {
		return false, err
	}
	f, err := normalizeBridgeVaultID(from)
	if err != nil {
		return false, fmt.Errorf("from: %w", err)
	}
	t, err := normalizeBridgeVaultID(to)
	if err != nil {
		return false, fmt.Errorf("to: %w", err)
	}
	dir := BridgesDir(denID)
	for _, name := range []string{bridgeFileName(f, t), bridgeFileName(t, f)} {
		path := filepath.Join(dir, name)
		if _, statErr := os.Stat(path); statErr == nil {
			if err := os.Remove(path); err != nil {
				return false, fmt.Errorf("remove den bridge: %w", err)
			}
			return true, nil
		}
	}
	return false, nil
}

// existingBridge finds a den bridge between f and t in either orientation.
func existingBridge(dir, f, t string) (path string, b *namespace.Bridge, ok bool) {
	for _, name := range []string{bridgeFileName(f, t), bridgeFileName(t, f)} {
		p := filepath.Join(dir, name)
		if bb, err := namespace.LoadBridge(p); err == nil {
			return p, bb, true
		}
	}
	return "", nil, false
}

// bridgeFileName is the @<from>--@<to>.md manifest name (matches the cross-vault
// bridge naming in namespace.writeCrossVaultBridgeTemp).
func bridgeFileName(from, to string) string {
	return fmt.Sprintf("@%s--@%s.md", from, to)
}

// normalizeBridgeVaultID strips a leading '@' and validates the id: non-empty,
// no path separators, no "..", no control characters (a hostile id would break
// the @a--@b.md filename or the YAML frontmatter it is interpolated into).
func normalizeBridgeVaultID(id string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(id), "@")
	if v == "" {
		return "", fmt.Errorf("vault id must not be empty")
	}
	if strings.ContainsAny(v, "/\\") {
		return "", fmt.Errorf("vault id %q must not contain path separators", v)
	}
	if strings.Contains(v, "..") {
		return "", fmt.Errorf("vault id %q must not contain \"..\"", v)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("vault id %q must not contain control characters", v)
		}
	}
	return v, nil
}

// dedupeRelations trims, drops empties, and dedupes preserving first-seen order.
func dedupeRelations(rels []string) []string {
	seen := make(map[string]bool, len(rels))
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// mergeRelations unions incoming into existing (existing order preserved, new
// ones appended) and reports whether any new relation was added.
func mergeRelations(existing, incoming []string) ([]string, bool) {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, r := range existing {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	changed := false
	for _, r := range incoming {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
		changed = true
	}
	return out, changed
}

// writeBridgeFile atomically writes a den bridge manifest (tmp+rename).
func writeBridgeFile(path string, b *namespace.Bridge) error {
	if b.Created == "" {
		b.Created = time.Now().UTC().Format(time.RFC3339)
	}
	yamlBytes, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal den bridge: %w", err)
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	fmt.Fprintf(&buf, "# Den Bridge: %s <-> %s\n", b.SourceVaultID, b.TargetVaultID)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create den bridges dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".bridge-*.md.tmp")
	if err != nil {
		return fmt.Errorf("create den bridge tmp: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(buf.String()); err != nil {
		return fmt.Errorf("write den bridge tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close den bridge tmp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod den bridge tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit den bridge: %w", err)
	}
	success = true
	return nil
}
