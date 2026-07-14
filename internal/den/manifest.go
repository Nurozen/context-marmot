package den

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

func parseManifest(data []byte) (*Manifest, string, error) {
	yamlBlock, body, err := frontmatter.Split(data)
	if err != nil {
		return nil, "", fmt.Errorf("den manifest frontmatter: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(yamlBlock, &m); err != nil {
		return nil, "", fmt.Errorf("parse den manifest yaml: %w", err)
	}
	if m.Version == 0 {
		m.Version = CurrentManifestVersion
	}
	// Normalize project paths to OS form for in-memory use; storage keeps slash-form.
	for i, p := range m.Projects {
		m.Projects[i] = filepath.FromSlash(p)
	}
	if m.Lifetime == "" {
		m.Lifetime = LifetimeDurable
	}
	return &m, body, nil
}

// checkManifestWritable refuses write if version exceeds the ceiling.
func checkManifestWritable(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("nil manifest")
	}
	if m.Version > CurrentManifestVersion {
		return fmt.Errorf("manifest version %d exceeds supported %d; upgrade marmot before editing this den", m.Version, CurrentManifestVersion)
	}
	return nil
}

// slashProjects returns projects in storage slash-form.
func slashProjects(projects []string) []string {
	out := make([]string, len(projects))
	for i, p := range projects {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

// joinBody ensures body is written after frontmatter without double newlines issues.
func joinBody(body string) string {
	return strings.TrimPrefix(body, "\n")
}
