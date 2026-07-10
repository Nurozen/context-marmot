package warren

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentMountRMW is the lost-update regression for the workspace
// _warren.md read-modify-write: N concurrent Mount calls with distinct
// projects must all survive. Each updateWorkspaceState opens its own lock
// fd, and BSD flock is per open-file-description, so single-process
// goroutines exercise the real flock path.
func TestConcurrentMountRMW(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	const n = 8
	projects := make([]string, n)
	for i := range projects {
		projects[i] = fmt.Sprintf("project-%d", i)
	}
	writeWarrenFixture(t, warrenRoot, "product-platform", projects...)
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		project := projects[i]
		go func() {
			defer wg.Done()
			if _, err := Mount(workspace, "product-platform", []string{project}, false); err != nil {
				t.Errorf("Mount(%s): %v", project, err)
			}
		}()
	}
	wg.Wait()

	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	active := state.Warrens["product-platform"].ActiveProjects
	if len(active) != n {
		t.Fatalf("active projects = %v, want all %d (lost updates)", active, n)
	}
}

// TestConcurrentMountRMWMultiProcess is the same regression across real OS
// processes: each helper subprocess mounts one distinct project into the
// shared workspace; the union must survive.
func TestConcurrentMountRMWMultiProcess(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	const n = 4
	projects := make([]string, n)
	for i := range projects {
		projects[i] = fmt.Sprintf("project-%d", i)
	}
	writeWarrenFixture(t, warrenRoot, "product-platform", projects...)
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cmds := make([]*exec.Cmd, n)
	outs := make([]*bytes.Buffer, n)
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperMountProject$", "-test.v")
		cmd.Env = append(os.Environ(),
			"MARMOT_WARREN_MOUNT_HELPER=1",
			"MARMOT_WARREN_TEST_WORKSPACE="+workspace,
			"MARMOT_WARREN_TEST_PROJECT="+projects[i],
		)
		outs[i] = &bytes.Buffer{}
		cmd.Stdout = outs[i]
		cmd.Stderr = outs[i]
		cmds[i] = cmd
	}
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
	}
	for i, cmd := range cmds {
		err := cmd.Wait()
		if err != nil || !strings.Contains(outs[i].String(), "HELPER_MOUNTED") {
			t.Fatalf("helper %d failed: %v\n%s", i, err, outs[i].String())
		}
	}

	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	active := state.Warrens["product-platform"].ActiveProjects
	if len(active) != n {
		t.Fatalf("active projects = %v, want all %d (cross-process lost updates)", active, n)
	}
}

// TestHelperMountProject is the subprocess body for the multi-process test.
func TestHelperMountProject(t *testing.T) {
	if os.Getenv("MARMOT_WARREN_MOUNT_HELPER") != "1" {
		t.Skip("helper process only")
	}
	workspace := os.Getenv("MARMOT_WARREN_TEST_WORKSPACE")
	project := os.Getenv("MARMOT_WARREN_TEST_PROJECT")
	if _, err := Mount(workspace, "product-platform", []string{project}, false); err != nil {
		fmt.Printf("HELPER_ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("HELPER_MOUNTED")
}

// TestConcurrentManifestRMW: concurrent AddBridge calls through
// updateManifest must not drop each other's bridges.
func TestConcurrentManifestRMW(t *testing.T) {
	root := t.TempDir()
	const n = 6
	projects := make([]string, n+1)
	for i := range projects {
		projects[i] = fmt.Sprintf("project-%d", i)
	}
	writeWarrenFixture(t, root, "product-platform", projects...)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		target := projects[i+1]
		go func() {
			defer wg.Done()
			if _, err := AddBridge(root, Bridge{Source: "project-0", Target: target, Relations: []string{"calls"}}); err != nil {
				t.Errorf("AddBridge(%s): %v", target, err)
			}
		}()
	}
	wg.Wait()

	manifest, _, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Bridges) != n {
		t.Fatalf("bridges = %d, want %d (lost updates): %+v", len(manifest.Bridges), n, manifest.Bridges)
	}
}
