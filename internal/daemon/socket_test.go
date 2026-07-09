package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathPrimary(t *testing.T) {
	dataDir := "/tmp/vault/.marmot-data" // comfortably under the limit
	got := SocketPath(dataDir)
	want := filepath.Join(dataDir, socketFileName)
	if got != want {
		t.Fatalf("SocketPath(%q) = %q, want %q", dataDir, got, want)
	}
}

func TestSocketPathFallback(t *testing.T) {
	// Build an absolute dataDir whose socket path exceeds maxSocketPathLen —
	// the macOS sun_path cap would reject it, so the hash fallback must kick in.
	long := filepath.Join("/tmp", strings.Repeat("deeply-nested-vault-dir/", 5), ".marmot-data")
	if len(filepath.Join(long, socketFileName)) <= maxSocketPathLen {
		t.Fatalf("test setup: path %q not long enough", long)
	}

	got := SocketPath(long)
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("fallback path %q not under os.TempDir() %q", got, os.TempDir())
	}
	base := filepath.Base(got)
	if !strings.HasPrefix(base, "marmot-") || !strings.HasSuffix(base, ".sock") {
		t.Fatalf("fallback basename %q, want marmot-<hash>.sock", base)
	}
	// marmot- + 16 hex chars + .sock
	if len(base) != len("marmot-")+16+len(".sock") {
		t.Fatalf("fallback basename %q: hash not 16 hex chars", base)
	}
	if len(got) > 104 {
		t.Fatalf("fallback path %q exceeds sun_path limit (%d bytes)", got, len(got))
	}

	// Deterministic for the same vault.
	if again := SocketPath(long); again != got {
		t.Fatalf("SocketPath not deterministic: %q vs %q", got, again)
	}
	// Distinct vaults hash to distinct sockets (hermetic tests never collide
	// with a developer's real vaults).
	other := filepath.Join("/tmp", strings.Repeat("other-nested-vault-dirs/", 5), ".marmot-data")
	if SocketPath(other) == got {
		t.Fatalf("distinct vaults mapped to the same fallback socket %q", got)
	}
}
