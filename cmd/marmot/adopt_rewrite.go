package main

// den adopt MCP-config rewrites (OQ13 decided YES): after the in-repo vault
// moves into the den, project-local MCP configs written by `marmot setup`
// still embed `serve --dir <old-vault-abs>`. Adopt rewrites the context-marmot
// entry in each of the four generators' outputs (.mcp.json, .codex/config.toml,
// .vscode/mcp.json, .cursor/mcp.json) to `serve --den <den-id>`, preserving
// every unrelated key. Unparseable or non-matching files are warn+skip — a
// rewrite must never clobber user config.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// mcpRewrite is one planned config rewrite: Desc for dry-run ops, apply to
// perform the (atomic) write.
type mcpRewrite struct {
	Path  string
	Desc  string
	apply func() error
}

// planMCPRewrites inspects the four project-local MCP configs under
// projectRoot and returns the rewrites whose context-marmot entry embeds
// `--dir <oldVaultAbs>`, plus warnings for files that exist but could not be
// parsed. Files without a matching entry are skipped silently. Nothing is
// written until a plan's apply runs.
func planMCPRewrites(projectRoot, oldVaultAbs, denID string) (plans []mcpRewrite, warnings []string) {
	jsonConfigs := []struct {
		path string
		keys []string
	}{
		{filepath.Join(projectRoot, ".mcp.json"), []string{"mcpServers"}},
		{filepath.Join(projectRoot, ".vscode", "mcp.json"), []string{"servers"}},
		{filepath.Join(projectRoot, ".cursor", "mcp.json"), []string{"mcpServers"}},
	}
	for _, jc := range jsonConfigs {
		plan, warn := planJSONRewrite(jc.path, jc.keys, oldVaultAbs, denID)
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if plan != nil {
			plans = append(plans, *plan)
		}
	}
	if plan, warn := planCodexRewrite(filepath.Join(projectRoot, ".codex", "config.toml"), oldVaultAbs, denID); warn != "" {
		warnings = append(warnings, warn)
	} else if plan != nil {
		plans = append(plans, *plan)
	}
	return plans, warnings
}

// canonicalVaultPath canonicalizes a vault path for rewrite matching:
// Clean + EvalSymlinks when the path resolves, falling back to Clean alone.
// Adopt normalizes the project key through routes.NormalizeProjectKey
// (EvalSymlinks), while configs written by `marmot setup` embed the path the
// user typed — on macOS /tmp and /var are symlinks into /private, so a
// Clean-only comparison silently skips the rewrite for such projects.
func canonicalVaultPath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	// The path may no longer exist — adopt plans rewrites AFTER the vault
	// moved into the den — so resolve the nearest existing ancestor and
	// re-append the remainder (enough to see through /tmp → /private/tmp).
	dir, base := filepath.Dir(clean), filepath.Base(clean)
	if dir == clean || base == string(filepath.Separator) || base == "." {
		return clean
	}
	return filepath.Join(canonicalVaultPath(dir), base)
}

// sameVaultPath reports whether two vault paths name the same location after
// symlink resolution (Clean-only fallback when either does not resolve).
func sameVaultPath(a, b string) bool {
	return canonicalVaultPath(a) == canonicalVaultPath(b)
}

// argsRewriteServeDen replaces the `--dir <oldVaultAbs>` pair in a serve args
// list with `--den <denID>`. Returns (newArgs, true) only when the pair was
// present. Paths are compared symlink-resolved (sameVaultPath) on BOTH sides.
func argsRewriteServeDen(args []string, oldVaultAbs, denID string) ([]string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--dir" && sameVaultPath(args[i+1], oldVaultAbs) {
			out := make([]string, 0, len(args))
			out = append(out, args[:i]...)
			out = append(out, "--den", denID)
			out = append(out, args[i+2:]...)
			return out, true
		}
	}
	return nil, false
}

// planJSONRewrite prepares the rewrite for one JSON MCP config: walk keys to
// the server map, and when its context-marmot entry's args carry
// `--dir <oldVaultAbs>`, plan an atomic write with `--den <denID>` instead.
// Returns (nil, warning) for an unparseable file, (nil, "") when there is
// nothing to rewrite.
func planJSONRewrite(path string, keys []string, oldVaultAbs, denID string) (*mcpRewrite, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "" // missing config is the normal case
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Sprintf("skip %s: not valid JSON (%v)", path, err)
	}
	cur := doc
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, ""
		}
		cur = next
	}
	entry, ok := cur["context-marmot"].(map[string]any)
	if !ok {
		return nil, ""
	}
	rawArgs, ok := entry["args"].([]any)
	if !ok {
		return nil, ""
	}
	args := make([]string, 0, len(rawArgs))
	for _, a := range rawArgs {
		s, ok := a.(string)
		if !ok {
			return nil, fmt.Sprintf("skip %s: context-marmot args are not all strings", path)
		}
		args = append(args, s)
	}
	newArgs, matched := argsRewriteServeDen(args, oldVaultAbs, denID)
	if !matched {
		return nil, ""
	}
	out := make([]any, len(newArgs))
	for i, a := range newArgs {
		out[i] = a
	}
	entry["args"] = out
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Sprintf("skip %s: %v", path, err)
	}
	payload = append(payload, '\n')
	return &mcpRewrite{
		Path: path,
		Desc: fmt.Sprintf("rewrite %s: serve --dir %s -> serve --den %s", path, oldVaultAbs, denID),
		apply: func() error {
			return atomicWriteFile(path, payload)
		},
	}, ""
}

// planCodexRewrite prepares the rewrite for .codex/config.toml. There is no
// TOML dependency, so it does a strictly-shaped line rewrite: inside the
// [mcp_servers.context-marmot] section, the exact args line the project
// generator writes (`args = ["serve", "--dir", "<old>"]`) becomes
// `args = ["serve", "--den", "<id>"]`. Anything else is left untouched.
func planCodexRewrite(path, oldVaultAbs, denID string) (*mcpRewrite, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	lines := strings.Split(string(data), "\n")
	newArgs := fmt.Sprintf(`args = ["serve", "--den", %q]`, denID)
	inSection := false
	matched := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inSection = trimmed == "[mcp_servers.context-marmot]"
			continue
		}
		if !inSection {
			continue
		}
		// Generated shape: args = ["serve", "--dir", "<path>"]. The embedded
		// path is compared symlink-resolved (sameVaultPath), not literally —
		// the config keeps the path the user typed (e.g. /tmp/…) while adopt
		// normalized through EvalSymlinks (/private/tmp/… on macOS).
		if dirPath, ok := codexServeDirArg(trimmed); ok && sameVaultPath(dirPath, oldVaultAbs) {
			lines[i] = strings.Replace(line, trimmed, newArgs, 1)
			matched = true
		}
	}
	if !matched {
		// The section may exist with a hand-edited args shape; warn so the
		// user knows adopt did not touch it.
		if strings.Contains(string(data), "[mcp_servers.context-marmot]") &&
			strings.Contains(string(data), oldVaultAbs) {
			return nil, fmt.Sprintf("skip %s: context-marmot entry references the old vault but not in the generated shape; update it manually to `serve --den %s`", path, denID)
		}
		return nil, ""
	}
	payload := []byte(strings.Join(lines, "\n"))
	return &mcpRewrite{
		Path: path,
		Desc: fmt.Sprintf("rewrite %s: serve --dir %s -> serve --den %s", path, oldVaultAbs, denID),
		apply: func() error {
			return atomicWriteFile(path, payload)
		},
	}, ""
}

// codexServeDirArg extracts the quoted --dir path from the generated codex
// args line `args = ["serve", "--dir", "<path>"]` (exact shape the project
// generator writes; anything else returns ok=false).
func codexServeDirArg(trimmedLine string) (dirPath string, ok bool) {
	const prefix = `args = ["serve", "--dir", `
	const suffix = `]`
	if !strings.HasPrefix(trimmedLine, prefix) || !strings.HasSuffix(trimmedLine, suffix) {
		return "", false
	}
	quoted := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmedLine, prefix), suffix))
	unquoted, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return unquoted, true
}
