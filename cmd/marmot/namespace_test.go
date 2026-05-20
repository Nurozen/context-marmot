package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNamespaceCreateListUpdateRemove(t *testing.T) {
	vault := initTestVault(t)

	code := run([]string{"namespace", "create", "team-api", "--dir", vault, "--root-path", "../team-api"})
	if code != 0 {
		t.Fatalf("namespace create exit code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(vault, "team-api", "_namespace.md")); err != nil {
		t.Fatalf("expected namespace manifest: %v", err)
	}

	code = run([]string{"namespace", "list", "--dir", vault, "--json"})
	if code != 0 {
		t.Fatalf("namespace list exit code = %d", code)
	}

	code = run([]string{"namespace", "update", "--dir", vault, "--root-path", "../renamed", "team-api"})
	if code != 0 {
		t.Fatalf("namespace update exit code = %d", code)
	}

	code = run([]string{"namespace", "remove", "--dir", vault, "team-api"})
	if code != 0 {
		t.Fatalf("namespace remove exit code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(vault, "team-api", "_namespace.md")); !os.IsNotExist(err) {
		t.Fatalf("expected namespace manifest removed, stat err=%v", err)
	}
}

func TestNamespaceDoctorDetectsImplicitNamespace(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "implicit/overview", "implicit")

	code := run([]string{"namespace", "doctor", "--dir", vault})
	if code == 0 {
		t.Fatal("expected namespace doctor to fail for implicit namespace without manifest")
	}

	code = run([]string{"namespace", "create", "--dir", vault, "implicit"})
	if code != 0 {
		t.Fatalf("namespace create exit code = %d", code)
	}
	code = run([]string{"namespace", "doctor", "--dir", vault})
	if code != 0 {
		t.Fatalf("namespace doctor exit code after create = %d", code)
	}
}

func TestNamespaceRemoveRefusesNodesWithoutForce(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "team-api/overview", "team-api")
	if code := run([]string{"namespace", "create", "--dir", vault, "team-api"}); code != 0 {
		t.Fatalf("namespace create exit code = %d", code)
	}

	code := run([]string{"namespace", "remove", "--dir", vault, "team-api"})
	if code == 0 {
		t.Fatal("expected namespace remove to fail while nodes still reference namespace")
	}
	code = run([]string{"namespace", "remove", "--dir", vault, "--force", "team-api"})
	if code != 0 {
		t.Fatalf("namespace remove --force exit code = %d", code)
	}
}

func TestIndexDirFlagAfterSource(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, ".marmot")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(vault, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `---
version: "1"
namespace: indexed-after
embedding_provider: mock
embedding_model: test-model
---
`
	if err := os.WriteFile(filepath.Join(vault, "_config.md"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	source := "package main\n\nfunc Hello() string { return \"hello\" }\n"
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	code := run([]string{"index", src, "--dir", vault})
	if code != 0 {
		t.Fatalf("index exit code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(vault, "indexed-after", "_namespace.md")); err != nil {
		t.Fatalf("expected namespace manifest in --dir vault: %v", err)
	}
}
