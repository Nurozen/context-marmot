package warrenreg

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateCrossProcessNoLostWrites is the lost-update regression for the
// warrens.yml read-modify-write (mirror of routes' TestUpdateCrossProcessNoLostWrites):
// two subprocesses run Update(add-unique-warren) loops against one MARMOT_HOME;
// with the flock in Update the union of ids must survive.
func TestUpdateCrossProcessNoLostWrites(t *testing.T) {
	homeDir := t.TempDir()
	const procs = 2
	const keysPerProc = 20

	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := 0; i < procs; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperWarrenRegUpdater$", "-test.v")
		cmd.Env = append(os.Environ(),
			"MARMOT_WARRENREG_UPDATE_HELPER=1",
			"MARMOT_HOME="+homeDir,
			fmt.Sprintf("MARMOT_WARRENREG_TEST_PREFIX=proc%d", i),
			fmt.Sprintf("MARMOT_WARRENREG_TEST_KEYS=%d", keysPerProc),
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
		if err := cmd.Wait(); err != nil || !strings.Contains(outs[i].String(), "HELPER_DONE") {
			t.Fatalf("helper %d failed: %v\n%s", i, err, outs[i].String())
		}
	}

	t.Setenv("MARMOT_HOME", homeDir)
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < procs; i++ {
		for k := 0; k < keysPerProc; k++ {
			id := fmt.Sprintf("proc%d-warren-%d", i, k)
			if _, ok := reg.Warrens[id]; !ok {
				t.Errorf("id %s lost (have %d ids total)", id, len(reg.Warrens))
			}
		}
	}

	// No stray temp files linger next to the registry.
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(homeDir, "warrens.yml")); err != nil {
		t.Fatalf("registry file missing: %v", err)
	}
}

// TestHelperWarrenRegUpdater is the subprocess body for the cross-process test.
func TestHelperWarrenRegUpdater(t *testing.T) {
	if os.Getenv("MARMOT_WARRENREG_UPDATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	prefix := os.Getenv("MARMOT_WARRENREG_TEST_PREFIX")
	var keys int
	if _, err := fmt.Sscanf(os.Getenv("MARMOT_WARRENREG_TEST_KEYS"), "%d", &keys); err != nil {
		fmt.Printf("HELPER_ERROR: bad key count: %v\n", err)
		os.Exit(1)
	}
	for i := 0; i < keys; i++ {
		id := fmt.Sprintf("%s-warren-%d", prefix, i)
		if err := Update(func(reg *Registry) error {
			reg.Warrens[id] = Entry{URL: "https://example.com/" + id + ".git", DefaultBranch: "main"}
			return nil
		}); err != nil {
			fmt.Printf("HELPER_ERROR: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println("HELPER_DONE")
}
