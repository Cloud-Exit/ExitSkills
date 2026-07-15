package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		messages, ok := request["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages = %#v", request["messages"])
		}
		system, ok := messages[0].(map[string]any)["content"].(string)
		if !ok || !strings.Contains(strings.ToLower(system), "untrusted data") || !strings.Contains(strings.ToLower(system), "quality") {
			t.Fatalf("system prompt does not establish an untrusted-data boundary: %q", system)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"score":8,"qualityScore":7,"status":"pass","summary":"Safe and actionable","riskLevel":"LOW","categories":["SAFE"]}`}}},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret", "auditor", server.Client())
	result, err := client.Audit(context.Background(), "# Safe skill")
	if err != nil {
		t.Fatal(err)
	}
	if result.Score != 8 || result.QualityScore != 7 || result.Status != "pass" || result.RiskLevel != "LOW" {
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

func TestAuditDoesNotExposeUpstreamResponseBodyInErrors(t *testing.T) {
	const sensitiveMarker = "skill-content-must-not-be-logged"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, sensitiveMarker, http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "auditor", server.Client())
	_, err := client.Audit(context.Background(), "content")
	if err == nil {
		t.Fatal("Audit() error = nil, want upstream error")
	}
	if strings.Contains(err.Error(), sensitiveMarker) {
		t.Fatalf("Audit() error exposed upstream response body: %v", err)
	}
}

func TestAuditRejectsUnknownRiskLevel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":8,\"qualityScore\":8,\"status\":\"pass\",\"summary\":\"safe\",\"riskLevel\":\"TRUST_ME\",\"categories\":[]}"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "auditor", server.Client())
	if _, err := client.Audit(context.Background(), "content"); err == nil {
		t.Fatal("Audit() error = nil, want risk-level validation error")
	}
}

func TestAuditRejectsMissingQualityAssessment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":8,\"status\":\"pass\",\"summary\":\"safe\",\"riskLevel\":\"LOW\",\"categories\":[]}"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "auditor", server.Client())
	if _, err := client.Audit(context.Background(), "content"); err == nil {
		t.Fatal("Audit() error = nil, want missing quality assessment error")
	}
}

func TestAuditRejectsUnboundedModelOutputFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, _ := json.Marshal(Result{Score: 8, Status: "pass", Summary: strings.Repeat("a", 2001), RiskLevel: "LOW"})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(result)}}}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "auditor", server.Client())
	if _, err := client.Audit(context.Background(), "content"); err == nil {
		t.Fatal("Audit() error = nil, want oversized summary rejection")
	}
}

func TestAuditRejectsUnboundedCategories(t *testing.T) {
	categories := make([]string, 33)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, _ := json.Marshal(Result{Score: 8, Status: "pass", Summary: "safe", RiskLevel: "LOW", Categories: categories})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(result)}}}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "auditor", server.Client())
	if _, err := client.Audit(context.Background(), "content"); err == nil {
		t.Fatal("Audit() error = nil, want oversized categories rejection")
	}
}
