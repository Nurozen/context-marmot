package routes

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestUpdateCrossProcessNoLostWrites is the lost-update regression for the
// routes.yml read-modify-write: two subprocesses run Update(Set(uniqueKey))
// loops against one file; with the flock in Update the union of keys must
// survive.
func TestUpdateCrossProcessNoLostWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.yml")
	const procs = 2
	const keysPerProc = 20

	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := 0; i < procs; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperRoutesUpdater$", "-test.v")
		cmd.Env = append(os.Environ(),
			"MARMOT_ROUTES_UPDATE_HELPER=1",
			"MARMOT_ROUTES="+path,
			fmt.Sprintf("MARMOT_ROUTES_TEST_PREFIX=proc%d", i),
			fmt.Sprintf("MARMOT_ROUTES_TEST_KEYS=%d", keysPerProc),
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

	rt, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	got := rt.List()
	for i := 0; i < procs; i++ {
		for k := 0; k < keysPerProc; k++ {
			key := fmt.Sprintf("proc%d-vault-%d", i, k)
			if _, ok := got[key]; !ok {
				t.Errorf("key %s lost (have %d keys total)", key, len(got))
			}
		}
	}
}

// TestHelperRoutesUpdater is the subprocess body for the cross-process test.
func TestHelperRoutesUpdater(t *testing.T) {
	if os.Getenv("MARMOT_ROUTES_UPDATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	prefix := os.Getenv("MARMOT_ROUTES_TEST_PREFIX")
	var keys int
	if _, err := fmt.Sscanf(os.Getenv("MARMOT_ROUTES_TEST_KEYS"), "%d", &keys); err != nil {
		fmt.Printf("HELPER_ERROR: bad key count: %v\n", err)
		os.Exit(1)
	}
	for i := 0; i < keys; i++ {
		key := fmt.Sprintf("%s-vault-%d", prefix, i)
		if err := Update(func(rt *RoutingTable) error {
			rt.Set(key, "/tmp/"+key)
			return nil
		}); err != nil {
			fmt.Printf("HELPER_ERROR: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println("HELPER_DONE")
}

// TestConcurrentSaveToNoTmpCollision: SaveTo now uses uniquely named temp
// files, so concurrent saves to one path never collide on a shared .tmp name
// and never leave stray temp files behind.
func TestConcurrentSaveToNoTmpCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			rt := &RoutingTable{Vaults: map[string]VaultEntry{
				fmt.Sprintf("vault-%d", i): {Path: "/tmp/x"},
			}}
			if err := SaveTo(rt, path); err != nil {
				t.Errorf("SaveTo: %v", err)
			}
		}()
	}
	wg.Wait()

	// The final file parses, and no temp files linger.
	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom after concurrent saves: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}
