// Package contracts_test pins the OQ15 JSON envelope fixtures under testdata/contracts.
// Fixtures pin schema:1 shapes for dens/routes JSON verbs. Implementation
// tests exercise live CLI envelopes; these files remain the shared contract sketches.
package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func TestContractFixturesExistAndSchema1(t *testing.T) {
	dir := fixtureDir(t)
	want := []string{
		"den_create.v1.json",
		"den_create_no_pointer.v1.json",
		"den_create_with_links.v1.json",
		"den_create_duplicate.v1.json",
		"den_create_project_collision.v1.json",
		"den_status.v1.json",
		"den_list.v1.json",
		"den_destroy.v1.json",
		"den_destroy_contributed.v1.json",
		"dry_run.v1.json",
		"error.v1.json",
		"route_set_project.v1.json",
		"warren_status_additive.v1.json",
	}
	for _, name := range want {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing contract fixture %s: %v", name, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: invalid JSON: %v", name, err)
		}
		schema, ok := doc["schema"]
		if !ok {
			t.Fatalf("%s: missing schema field", name)
		}
		// encoding/json numbers are float64
		n, ok := schema.(float64)
		if !ok || int(n) != 1 {
			t.Fatalf("%s: schema = %v, want 1", name, schema)
		}
	}
}

func TestDenCreateLinksResolvedVia(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_with_links.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Links  []struct {
			ResolvedVia string `json:"resolved_via"`
		} `json:"links"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"warren-url": true, "checkout-vault": true, "none": true}
	got := map[string]bool{}
	for _, l := range doc.Links {
		got[l.ResolvedVia] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("den_create_with_links.v1.json missing resolved_via %q", k)
		}
	}
}

func TestDenCreateNoPointerWrittenFalse(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_no_pointer.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema         int  `json:"schema"`
		PointerWritten bool `json:"pointer_written"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PointerWritten {
		t.Fatal("stave attach fixture must have pointer_written: false")
	}
}

func TestErrorEnvelopeHasCodeAndMessage(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "error.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error == nil || doc.Error.Code == "" || doc.Error.Message == "" {
		t.Fatalf("error envelope incomplete: %+v", doc.Error)
	}
}

func TestDenDestroyContributedField(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_destroy_contributed.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Contributed *struct {
			Added int `json:"added"`
		} `json:"contributed"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Contributed == nil {
		t.Fatal("expected contributed object")
	}
	if doc.Contributed.Added != 2 {
		t.Fatalf("contributed.added = %d, want 2", doc.Contributed.Added)
	}
}

func TestProjectCollisionErrorCode(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_project_collision.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error == nil || doc.Error.Code != "den_create_failed" {
		t.Fatalf("collision envelope: %+v", doc.Error)
	}
	if !contains(doc.Error.Message, "already registered") {
		t.Fatalf("message = %q", doc.Error.Message)
	}
}

func TestDenListEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_list.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int      `json:"schema"`
		Dens   []string `json:"dens"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Dens) < 1 {
		t.Fatal("dens list empty")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}
