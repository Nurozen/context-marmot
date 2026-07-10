package main

import "testing"

// hermeticEngine builds an engine for tests without touching the developer's
// real ~/.marmot/routes.yml (MARMOT_ROUTES=off) or real API keys. Every test
// in this package that calls buildEngine must go through this helper so a
// poisoned or populated global routing table can never leak real vaults into
// a test run. Cleanup is registered on t automatically.
func hermeticEngine(t *testing.T, dir string) *engineResult {
	t.Helper()
	t.Setenv("MARMOT_ROUTES", "off")
	t.Setenv("ANTHROPIC_API_KEY", "")
	result, err := buildEngine(dir)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
	t.Cleanup(result.Cleanup)
	return result
}
