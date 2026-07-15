package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGenerateEmbedsOpenAPIInRedocHTML(t *testing.T) {
	spec := []byte(`{"openapi":"3.1.0","info":{"title":"ExitMesh </script>","version":"test"},"paths":{}}`)
	var output bytes.Buffer
	if err := generate(&output, spec, "v2.5.3"); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, expected := range []string{
		"<!doctype html>",
		"Redoc.init(spec",
		"https://cdn.redoc.ly/redoc/v2.5.3/bundles/redoc.standalone.js",
		`ExitMesh \u003c/script\u003e`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("generated HTML is missing %q:\n%s", expected, html)
		}
	}
	if strings.Contains(html, "ExitMesh </script>") {
		t.Fatal("OpenAPI JSON was embedded without script-safe escaping")
	}
}

func TestGenerateRejectsInvalidOpenAPIJSON(t *testing.T) {
	if err := generate(&bytes.Buffer{}, []byte(`{"openapi":`), "v2.5.3"); err == nil {
		t.Fatal("generate() error = nil, want invalid JSON error")
	}
}

func TestOpenAPISpecCoversServiceRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title string `json:"title"`
		} `json:"info"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info.Title != "ExitSkills API" {
		t.Fatalf("info.title = %q, want ExitSkills API", spec.Info.Title)
	}
	if count := strings.Count(string(raw), `"qualityScore"`); count < 4 {
		t.Fatalf("OpenAPI spec contains qualityScore %d times, want it in catalog, detail, audit, and stats schemas", count)
	}
	if count := strings.Count(string(raw), `"llmChecked"`); count < 4 {
		t.Fatalf("OpenAPI spec contains llmChecked %d times, want assessment state in catalog, detail, audit, and stats schemas", count)
	}
	want := map[string][]string{
		"/v1/docs":                                {"get"},
		"/v1/skills":                              {"get"},
		"/v1/skills/search":                       {"get"},
		"/v1/skills/curated":                      {"get"},
		"/v1/skills/{owner}/{repo}/{skill}":       {"get"},
		"/v1/skills/audit/{owner}/{repo}/{skill}": {"get"},
		"/v1/admin/token":                         {"post", "delete"},
		"/v1/admin/stats":                         {"get"},
		"/healthz":                                {"get"},
		"/readyz":                                 {"get"},
	}
	for path, methods := range want {
		operations, exists := spec.Paths[path]
		if !exists {
			t.Errorf("OpenAPI spec is missing %s", path)
			continue
		}
		for _, method := range methods {
			if _, exists := operations[method]; !exists {
				t.Errorf("OpenAPI spec is missing %s %s", method, path)
			}
		}
	}
}
