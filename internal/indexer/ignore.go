package indexer

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// alwaysIgnore is the set of directory names that are always ignored,
// regardless of .gitignore content.
var alwaysIgnore = map[string]bool{
	".git":        true,
	".marmot":     true,
	"node_modules": true,
	"vendor":      true,
	"__pycache__": true,
}

// alwaysIgnoreFiles is the set of file names that are always ignored.
var alwaysIgnoreFiles = map[string]bool{
	".DS_Store": true,
}

// ignorePattern represents a single parsed gitignore pattern.
type ignorePattern struct {
	pattern  string // the glob pattern (after preprocessing)
	negation bool   // true if this is a ! (un-ignore) pattern
	dirOnly  bool   // true if the pattern ends with / (matches only directories)
	isDouble bool   // true if the pattern contains **
}

// IgnoreMatcher decides whether a given path should be ignored based on
// .gitignore rules, hardcoded defaults, and custom patterns.
type IgnoreMatcher struct {
	rootDir  string
	patterns []ignorePattern
}

// NewIgnoreMatcher reads .gitignore from rootDir (if it exists) and combines
// it with any extra patterns. The returned matcher can test paths relative to
// rootDir.
func NewIgnoreMatcher(rootDir string, extraPatterns []string) *IgnoreMatcher {
	m := &IgnoreMatcher{rootDir: rootDir}

	// Parse .gitignore if present.
	gitignorePath := filepath.Join(rootDir, ".gitignore")
	if f, err := os.Open(gitignorePath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if p, ok := parseLine(scanner.Text()); ok {
				m.patterns = append(m.patterns, p)
			}
		}
	}

	// Parse extra patterns.
	for _, raw := range extraPatterns {
		if p, ok := parseLine(raw); ok {
			m.patterns = append(m.patterns, p)
		}
	}

	return m
}

// ShouldIgnore returns true if the given path (relative to rootDir) should be
// skipped during indexing. isDir indicates whether the path is a directory.
func (m *IgnoreMatcher) ShouldIgnore(relPath string, isDir bool) bool {
	// Normalise separators.
	relPath = filepath.ToSlash(relPath)

	// Check hardcoded always-ignore directories and files.
	base := filepath.Base(relPath)
	if isDir && alwaysIgnore[base] {
		return true
	}
	if !isDir && alwaysIgnoreFiles[base] {
		return true
	}

	// Also check every path component against alwaysIgnore (handles nested
	// vendor/ etc.).
	for _, part := range strings.Split(relPath, "/") {
		if alwaysIgnore[part] {
			return true
		}
	}

	// Evaluate gitignore patterns in order; last matching pattern wins.
	ignored := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchPattern(p, relPath, base) {
			ignored = !p.negation
		}
	}

	return ignored
}

// parseLine parses a single gitignore line. Returns the pattern and true if
// the line is a valid, non-empty pattern; false if it should be skipped
// (blank line or comment).
func parseLine(line string) (ignorePattern, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignorePattern{}, false
	}

	var p ignorePattern

	// Handle negation.
	if strings.HasPrefix(line, "!") {
		p.negation = true
		line = line[1:]
	}

	// Handle directory-only patterns.
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}

	// Detect ** patterns.
	if strings.Contains(line, "**") {
		p.isDouble = true
	}

	p.pattern = line
	return p, true
}

// matchPattern checks whether a gitignore pattern matches the given path.
// relPath is the slash-separated path relative to the root; base is the
// filename component.
func matchPattern(p ignorePattern, relPath string, base string) bool {
	pat := p.pattern

	if p.isDouble {
		return matchDoubleGlob(pat, relPath)
	}

	// If the pattern contains a slash (other than trailing), it matches the
	// full relative path. Otherwise, it matches only the basename.
	if strings.Contains(pat, "/") {
		// Strip leading slash for matching.
		pat = strings.TrimPrefix(pat, "/")
		matched, _ := filepath.Match(pat, relPath)
		return matched
	}

	// Simple pattern: match against basename.
	matched, _ := filepath.Match(pat, base)
	return matched
}

// matchDoubleGlob handles patterns containing **, implementing recursive
// directory matching. It splits on ** and checks that all parts match
// contiguous segments of the path.
func matchDoubleGlob(pattern string, relPath string) bool {
	// Split pattern on "**" to get the prefix and suffix parts.
	parts := strings.Split(pattern, "**")

	switch len(parts) {
	case 1:
		// No ** found (shouldn't happen since isDouble was set).
		matched, _ := filepath.Match(pattern, relPath)
		return matched

	case 2:
		prefix := strings.TrimSuffix(parts[0], "/")
		suffix := strings.TrimPrefix(parts[1], "/")

		// **/ at the start means match any prefix.
		if prefix == "" && suffix == "" {
			return true
		}
		if prefix == "" {
			// **/suffix — match if any suffix of the path matches.
			segments := allSuffixes(relPath)
			for _, seg := range segments {
				if matched, _ := filepath.Match(suffix, seg); matched {
					return true
				}
			}
			return false
		}
		if suffix == "" {
			// prefix/** — match if the path starts with prefix.
			if matched, _ := filepath.Match(prefix, relPath); matched {
				return true
			}
			return strings.HasPrefix(relPath, prefix+"/")
		}

		// prefix/**/suffix — match if prefix matches the start and suffix
		// matches any remaining segment.
		if !strings.HasPrefix(relPath, prefix+"/") {
			if matched, _ := filepath.Match(prefix, relPath); !matched {
				return false
			}
		}
		rest := strings.TrimPrefix(relPath, prefix+"/")
		segments := allSuffixes(rest)
		for _, seg := range segments {
			if matched, _ := filepath.Match(suffix, seg); matched {
				return true
			}
		}
		return false

	default:
		// Multiple ** segments — fall back to a simple contains check
		// on non-glob literal parts.
		for _, part := range parts {
			part = strings.Trim(part, "/")
			if part == "" {
				continue
			}
			if !strings.Contains(relPath, part) {
				return false
			}
		}
		return true
	}
}

// allSuffixes returns the path itself plus every suffix obtained by stripping
// leading path components. E.g., "a/b/c" -> ["a/b/c", "b/c", "c"].
func allSuffixes(path string) []string {
	var suffixes []string
	suffixes = append(suffixes, path)
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			suffixes = append(suffixes, path[i+1:])
		}
	}
	return suffixes
}
