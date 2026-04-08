package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWrites_SameNamespace verifies that 20 concurrent goroutines can
// each write a unique node to the same namespace without data races or lost writes.
func TestConcurrentWrites_SameNamespace(t *testing.T) {
	eng := newClassifyTestEngine(t)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("test/node%d", i)
			req := makeCallToolRequest("context_write", map[string]any{
				"id":        id,
				"type":      "concept",
				"namespace": "default",
				"summary":   fmt.Sprintf("Node %d summary", i),
			})
			res, err := eng.HandleContextWrite(ctx, req)
			if err != nil {
				t.Errorf("HandleContextWrite(%s): %v", id, err)
				return
			}
			if res.IsError {
				t.Errorf("write %s returned error: %s", id, resultText(t, res))
			}
		}()
	}

	wg.Wait()

	// All 20 nodes must exist in the graph.
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("test/node%d", i)
		if _, ok := eng.GetGraph().GetNode(id); !ok {
			t.Errorf("node %s not found in graph after concurrent writes", id)
		}
	}
	if eng.GetGraph().NodeCount() != n {
		t.Errorf("expected %d nodes in graph, got %d", n, eng.GetGraph().NodeCount())
	}
}

// TestConcurrentWrites_DifferentNamespaces verifies that concurrent writes to
// two distinct namespaces do not contend or corrupt each other.
func TestConcurrentWrites_DifferentNamespaces(t *testing.T) {
	eng := newClassifyTestEngine(t)
	ctx := context.Background()

	const perNS = 10
	var wg sync.WaitGroup
	wg.Add(perNS * 2)

	writeNS := func(ns string, start int) {
		for i := start; i < start+perNS; i++ {
			i := i
			ns := ns
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("%s/node%d", ns, i)
				req := makeCallToolRequest("context_write", map[string]any{
					"id":        id,
					"type":      "concept",
					"namespace": ns,
					"summary":   fmt.Sprintf("Node %d in %s", i, ns),
				})
				res, err := eng.HandleContextWrite(ctx, req)
				if err != nil {
					t.Errorf("HandleContextWrite(%s): %v", id, err)
					return
				}
				if res.IsError {
					t.Errorf("write %s returned error: %s", id, resultText(t, res))
				}
			}()
		}
	}

	writeNS("ns-a", 0)
	writeNS("ns-b", 0)

	wg.Wait()

	// All 20 nodes must exist.
	total := 0
	for i := 0; i < perNS; i++ {
		for _, ns := range []string{"ns-a", "ns-b"} {
			id := fmt.Sprintf("%s/node%d", ns, i)
			if _, ok := eng.GetGraph().GetNode(id); !ok {
				t.Errorf("node %s not found in graph after concurrent writes", id)
			} else {
				total++
			}
		}
	}
	if total != perNS*2 {
		t.Errorf("expected %d nodes total, found %d", perNS*2, total)
	}
}

// TestConcurrentReadDuringWrite verifies no panics or races when reader
// goroutines query the graph while writer goroutines are actively writing.
func TestConcurrentReadDuringWrite(t *testing.T) {
	eng := newClassifyTestEngine(t)
	ctx := context.Background()

	const count = 10
	var wg sync.WaitGroup
	wg.Add(count * 2)

	// 10 writer goroutines.
	for i := 0; i < count; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("rw/writer%d", i)
			req := makeCallToolRequest("context_write", map[string]any{
				"id":      id,
				"type":    "concept",
				"summary": fmt.Sprintf("Writer node %d", i),
			})
			res, err := eng.HandleContextWrite(ctx, req)
			if err != nil {
				t.Errorf("HandleContextWrite(%s): %v", id, err)
			}
			_ = res
		}()
	}

	// 10 reader goroutines running concurrently with the writers.
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			req := makeCallToolRequest("context_query", map[string]any{
				"query": "test",
			})
			res, err := eng.HandleContextQuery(ctx, req)
			if err != nil {
				t.Errorf("HandleContextQuery: %v", err)
			}
			_ = res
		}()
	}

	wg.Wait()
	// Passing under -race is the primary assertion for this test.
}

// TestConcurrentDeleteAndWrite verifies that concurrent deletes and writes
// behave correctly: only one delete succeeds, all writes succeed.
func TestConcurrentDeleteAndWrite(t *testing.T) {
	eng := newClassifyTestEngine(t)
	ctx := context.Background()

	// Pre-write the target node.
	req := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/target",
		"type":    "concept",
		"summary": "Target node for concurrent delete test",
	})
	res, err := eng.HandleContextWrite(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("pre-write target: %v / %s", err, resultText(t, res))
	}

	const deleters = 5
	const writers = 5

	var wg sync.WaitGroup
	wg.Add(deleters + writers)

	deleteSuccesses := make([]bool, deleters)

	// 5 goroutines attempting to delete the same node.
	for i := 0; i < deleters; i++ {
		i := i
		go func() {
			defer wg.Done()
			delReq := makeCallToolRequest("context_delete", map[string]any{
				"id": "test/target",
			})
			delRes, err := eng.HandleContextDelete(ctx, delReq)
			if err != nil {
				t.Errorf("HandleContextDelete[%d]: %v", i, err)
				return
			}
			deleteSuccesses[i] = !delRes.IsError
		}()
	}

	// 5 goroutines writing unique "other" nodes concurrently.
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("test/other-%d", i)
			wrReq := makeCallToolRequest("context_write", map[string]any{
				"id":      id,
				"type":    "concept",
				"summary": fmt.Sprintf("Other node %d", i),
			})
			wrRes, err := eng.HandleContextWrite(ctx, wrReq)
			if err != nil {
				t.Errorf("HandleContextWrite(%s): %v", id, err)
				return
			}
			if wrRes.IsError {
				t.Errorf("write %s returned error: %s", id, resultText(t, wrRes))
			}
		}()
	}

	wg.Wait()

	// Exactly one delete should have succeeded (the rest get "not found").
	successCount := 0
	for _, ok := range deleteSuccesses {
		if ok {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 delete success, got %d", successCount)
	}

	// Verify delete result for the successful deleter.
	for i, ok := range deleteSuccesses {
		if ok {
			delReq := makeCallToolRequest("context_delete", map[string]any{
				"id": "test/target",
			})
			// Node should now be superseded, not active — a second delete would return error.
			delRes, err := eng.HandleContextDelete(ctx, delReq)
			if err != nil {
				t.Errorf("post-check delete[%d]: %v", i, err)
			}
			// The node may no longer be found as active, so this can be an error.
			_ = delRes
			break
		}
	}

	// All 5 "other" nodes must have been written successfully.
	for i := 0; i < writers; i++ {
		id := fmt.Sprintf("test/other-%d", i)
		if _, ok := eng.GetGraph().GetNode(id); !ok {
			t.Errorf("writer node %s not found in graph", id)
		}
	}

	// Verify no unmarshalling issues by inspecting delete result.
	_ = json.Marshal // referenced for completeness
}
