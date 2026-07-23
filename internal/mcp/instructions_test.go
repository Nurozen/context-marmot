package mcp

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/nurozen/context-marmot/internal/den"
)

// denVaultFixture builds a den directory (…/dens/<id>/ shape not required —
// detection keys on _den.md placement, not on $MARMOT_HOME) with an identity
// vault subdir and returns the vault path.
func denVaultFixture(t *testing.T, manifest string) string {
	t.Helper()
	denRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(denRoot, "_den.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	vaultDir := filepath.Join(denRoot, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return vaultDir
}

const denManifestFixture = `---
den_id: acme-den
version: 1
lifetime: task
links:
  - target: platform-core
    mode: edit
  - target: shared-protos
    mode: link
  - target: infra-live
    mode: live
---
# Den acme-den
`

func TestCollectTopologyDenVault(t *testing.T) {
	vaultDir := denVaultFixture(t, denManifestFixture)
	eng := &Engine{MarmotDir: vaultDir, LocalVaultID: "acme-den-vault"}

	snap := collectTopology(eng)

	if snap.VaultID != "acme-den-vault" {
		t.Errorf("VaultID = %q, want acme-den-vault", snap.VaultID)
	}
	if snap.NamespaceCount != 1 {
		t.Errorf("NamespaceCount = %d, want 1 (single-namespace mode)", snap.NamespaceCount)
	}
	if snap.Den == nil {
		t.Fatal("Den = nil, want den topology from ../_den.md")
	}
	if snap.Den.ID != "acme-den" || snap.Den.Lifetime != "task" {
		t.Errorf("Den = {%s %s}, want {acme-den task}", snap.Den.ID, snap.Den.Lifetime)
	}
	if len(snap.Den.Links) != 3 {
		t.Fatalf("len(Links) = %d, want 3", len(snap.Den.Links))
	}
	if snap.Den.Links[0].Target != "platform-core" || snap.Den.Links[0].Mode != "edit" {
		t.Errorf("Links[0] = %+v, want platform-core/edit", snap.Den.Links[0])
	}
	if len(snap.Mounts) != 0 {
		t.Errorf("Mounts = %v, want none (no warren state in fixture)", snap.Mounts)
	}
}

func TestCollectTopologyLinksOnlyDenRoot(t *testing.T) {
	// Links-only dens have no vault/ subdir and are served from the den root:
	// _den.md sits IN the served dir, not one level up.
	denRoot := t.TempDir()
	manifest := "---\nden_id: links-only\nversion: 1\n---\n"
	if err := os.WriteFile(filepath.Join(denRoot, "_den.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{MarmotDir: denRoot}

	snap := collectTopology(eng)

	if snap.Den == nil {
		t.Fatal("Den = nil, want detection of _den.md in served dir")
	}
	if snap.Den.ID != "links-only" {
		t.Errorf("Den.ID = %q, want links-only", snap.Den.ID)
	}
	if snap.Den.Lifetime != "durable" {
		t.Errorf("Den.Lifetime = %q, want durable default", snap.Den.Lifetime)
	}
	if snap.VaultID != "" {
		t.Errorf("VaultID = %q, want empty (unidentified)", snap.VaultID)
	}
}

func TestCollectTopologyPlainVaultNoDen(t *testing.T) {
	eng := &Engine{MarmotDir: filepath.Join(t.TempDir(), ".marmot")}
	if err := os.MkdirAll(eng.MarmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := collectTopology(eng)
	if snap.Den != nil {
		t.Errorf("Den = %+v, want nil for a plain vault", snap.Den)
	}
}

func TestRenderInstructions(t *testing.T) {
	snap := topologySnapshot{
		VaultID:        "acme-den-vault",
		NamespaceCount: 2,
		Den: &denTopology{
			ID:       "acme-den",
			Lifetime: "durable",
			Links: []denLinkStatus{
				{Link: den.Link{Target: "platform-core", Mode: "edit"}, Resolved: true, ResolvedVaultID: "platform-core-vault"},
				{Link: den.Link{Target: "shared-protos", Mode: "link"}},
			},
		},
		Mounts: []mountTopology{
			{VaultID: "proj-a-vault", ProjectID: "proj-a", Editable: true},
			{VaultID: "proj-b-vault", ProjectID: "proj-b"},
			{VaultID: "acme-den-vault", ProjectID: "self-proj", SelfAlias: true},
		},
	}

	got := renderInstructions(snap)

	for _, want := range []string{
		"Vault: acme-den-vault (2 namespaces)",
		"Den: acme-den (lifetime: durable)",
		"link: platform-core mode=edit (resolved: @platform-core-vault)",
		"link: shared-protos mode=link (unresolved)",
		"Warren mounts:",
		"@proj-a-vault (editable)",
		"@proj-b-vault (read-only)",
		"@acme-den-vault (self — this workspace's live vault)",
		"Write policy: own vault = full CRUD",
		"read-only links reject writes",
		"qualified IDs (@vault-id/node-id)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q\nfull text:\n%s", want, got)
		}
	}
}

func TestRenderInstructionsUnidentifiedVault(t *testing.T) {
	got := renderInstructions(topologySnapshot{NamespaceCount: 1})
	if !strings.Contains(got, "Vault: unidentified") {
		t.Errorf("missing unidentified vault line:\n%s", got)
	}
	if strings.Contains(got, "Den:") || strings.Contains(got, "Warren mounts:") {
		t.Errorf("empty topology must omit den/mount sections:\n%s", got)
	}
}

// TestInitializeCarriesInstructions verifies the wire behavior: the
// initialize result of a server built by NewServer carries the topology
// instructions, including a warren mount registered before construction
// (snapshot-at-init).
func TestInitializeCarriesInstructions(t *testing.T) {
	eng := warrenEngine(t)
	warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")

	srv := NewServer(eng)

	serverStdinR, serverStdinW := io.Pipe()
	clientStdinR, clientStdinW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		stdio := server.NewStdioServer(srv.mcpServer)
		stdio.SetErrorLogger(log.New(io.Discard, "", 0))
		serverDone <- stdio.Listen(ctx, serverStdinR, clientStdinW)
	}()
	stderrR, stderrW := io.Pipe()
	go func() {
		<-ctx.Done()
		stderrW.Close()
	}()
	c := client.NewClient(transport.NewIO(clientStdinR, pipeWriteCloser{serverStdinW}, pipeReadCloser{stderrR}))
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		cancel()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
		}
	})

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "instructions-test", Version: "0.0.1"}
	res, err := c.Initialize(ctx, initReq)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	for _, want := range []string{
		"Vault: local-vault",
		"@proj-a-vault (read-only)",
		"Write policy: own vault = full CRUD",
	} {
		if !strings.Contains(res.Instructions, want) {
			t.Errorf("initialize instructions missing %q\nfull text:\n%s", want, res.Instructions)
		}
	}
}
