package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuditParsesOpenAICompatibleResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "auditor" {
			t.Errorf("model = %v", request["model"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"score":8,"status":"pass","summary":"No material risk","riskLevel":"LOW","categories":["SAFE"]}`}}},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret", "auditor", server.Client())
	result, err := client.Audit(context.Background(), "# Safe skill")
	if err != nil {
		t.Fatal(err)
	}
	if result.Score != 8 || result.Status != "pass" || result.RiskLevel != "LOW" {
		t.Fatalf("unexpected audit: %+v", result)
	}
}

func TestAuditRejectsMalformedResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not-json"}}]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", "auditor", server.Client())
	if _, err := client.Audit(context.Background(), "content"); err == nil {
		t.Fatal("Audit() error = nil, want malformed response error")
	}
}
