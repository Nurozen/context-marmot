package den

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

func hermeticFreshnessHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	t.Setenv("MARMOT_HOME", root)
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })
	return root
}

func TestLinkFreshnessNotePinned(t *testing.T) {
	hermeticFreshnessHome(t)
	const pin = "aabbccddeeff00112233445566778899aabbccdd"
	if err := warren.WriteCachePin("w", pin); err != nil {
		t.Fatal(err)
	}

	// Pin matches the cache: annotated but not stale.
	note := LinkFreshnessNote(Link{Target: "w/p", Mode: LinkModeLink, Warren: "w", Project: "p", PinnedRef: pin})
	if !strings.HasPrefix(note, "pinned@") || strings.Contains(note, "stale") {
		t.Fatalf("note = %q", note)
	}
	// Recorded pin differs from the current cache pin: stale.
	note = LinkFreshnessNote(Link{Target: "w/p", Mode: LinkModeLink, Warren: "w", Project: "p", PinnedRef: "0123456789ab0123456789ab0123456789ab0123"})
	if !strings.Contains(note, "stale") {
		t.Fatalf("stale note = %q", note)
	}
	// No pin anywhere: nothing to say.
	if note := LinkFreshnessNote(Link{Target: "x/p", Mode: LinkModeLink, Warren: "x", Project: "p"}); note != "" {
		t.Fatalf("empty-pin note = %q", note)
	}
	// Edit links carry no cheap note (git-backed numbers are CLI territory).
	if note := LinkFreshnessNote(Link{Target: "w/p", Mode: LinkModeEdit, Warren: "w", Project: "p"}); note != "" {
		t.Fatalf("edit note = %q", note)
	}
}

func TestLinkFreshnessNoteLive(t *testing.T) {
	hermeticFreshnessHome(t)
	if note := LinkFreshnessNote(Link{Target: "ghost-den", Mode: LinkModeLive}); note != "unreachable" {
		t.Fatalf("dead live note = %q", note)
	}
	if _, err := Create("live-target", CreateOptions{Lifetime: LifetimeDurable}); err != nil {
		t.Fatal(err)
	}
	if note := LinkFreshnessNote(Link{Target: "live-target", Mode: LinkModeLive}); note != "" {
		t.Fatalf("reachable live note = %q", note)
	}
	// A routed vault id is reachable too.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.Set("routed-vault", t.TempDir())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !LiveTargetReachable("routed-vault") {
		t.Fatal("routed vault must be reachable")
	}
}

func TestPinnedLinkSourceCommit(t *testing.T) {
	hermeticFreshnessHome(t)
	checkout := warren.CacheCheckoutPath("w")
	if err := warren.SaveManifest(checkout, &warren.Manifest{
		WarrenID: "w",
		Projects: []warren.Project{{ProjectID: "p", Path: "projects/p/.marmot", SourceCommit: "cafe1234"}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	l := Link{Target: "w/p", Mode: LinkModeLink, Warren: "w", Project: "p"}
	if got := PinnedLinkSourceCommit(l); got != "cafe1234" {
		t.Fatalf("source commit = %q", got)
	}
	if got := SkewNote(l); !strings.Contains(got, "cafe1234") || !strings.Contains(got, "vault snapshot from source commit") {
		t.Fatalf("skew note = %q", got)
	}
	// Unknown project: nothing.
	if got := PinnedLinkSourceCommit(Link{Target: "w/x", Mode: LinkModeLink, Warren: "w", Project: "x"}); got != "" {
		t.Fatalf("unknown project source commit = %q", got)
	}
}
