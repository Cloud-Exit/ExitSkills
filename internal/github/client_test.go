package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverFindsSkillAndMarksOfficialOwner(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	content := "---\nname: Demo Skill\ndescription: A useful demo\n---\n# Demo"
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "incomplete_results": false, "items": []any{map[string]any{"name": "SKILL.md", "path": "skills/demo/SKILL.md", "url": server.URL + "/file", "repository": map[string]any{"full_name": "acme/tool", "stargazers_count": 11}}}})
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "name": "SKILL.md", "path": "skills/demo/SKILL.md", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
	})
	mux.HandleFunc("/repos/acme/tool/contents/skills/demo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"type": "file", "name": "SKILL.md", "path": "skills/demo/SKILL.md", "url": server.URL + "/file", "size": len(content)}})
	})

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client := NewClient(server.URL, "token", server.URL+"/official", server.Client()).WithLogger(logger)
	items, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %+v", len(items), items)
	}
	if items[0].ID != "acme/tool/demo" || items[0].Name != "Demo Skill" || !items[0].Official || items[0].Stars != 11 {
		t.Fatalf("unexpected candidate: %+v", items[0])
	}
	output := logs.String()
	for _, expected := range []string{
		`"msg":"official catalog loading"`,
		`"msg":"official catalog loaded"`,
		`"msg":"github skill search started"`,
		`"msg":"github skill search page"`,
		`"msg":"github skill page fetch started"`,
		`"msg":"github skill page progress"`,
		`"msg":"github skill search page processed"`,
		`"msg":"github skill discovery complete"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %s in logs:\n%s", expected, output)
		}
	}
}

func TestDiscoverIncludesPopularUnofficialRepositorySkills(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/repositories", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "(SKILL.md OR SKILLS.md) in:readme stars:>10" {
			t.Errorf("repository search q = %q, want star-filtered skill filename README query", got)
		}
		if got := r.URL.Query().Get("sort"); got != "stars" {
			t.Errorf("repository search sort = %q, want stars", got)
		}
		if got := r.URL.Query().Get("order"); got != "desc" {
			t.Errorf("repository search order = %q, want desc", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "items": []any{map[string]any{"full_name": "JuliusBrussee/caveman", "stargazers_count": 89644, "default_branch": "main"}}})
	})
	mux.HandleFunc("/repos/JuliusBrussee/caveman/git/trees/main", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []any{
			map[string]any{"path": "skills/cavecrew/SKILL.md", "type": "blob", "url": server.URL + "/blob/cavecrew"},
			map[string]any{"path": "skills/caveman/SKILL.md", "type": "blob", "url": server.URL + "/blob/caveman"},
			map[string]any{"path": "README.md", "type": "blob", "url": server.URL + "/blob/readme"},
		}})
	})
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "incomplete_results": false, "items": []any{}})
	})
	mux.HandleFunc("/blob/", func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# " + name))})
	})
	mux.HandleFunc("/repos/JuliusBrussee/caveman/contents/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).WithConcurrency(4).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Source != "JuliusBrussee/caveman" || item.Stars != 89644 || item.Official {
			t.Fatalf("popular unofficial skill was classified incorrectly: %+v", item)
		}
	}
}

func TestDiscoverEnumeratesSkillsFromOfficialRepositories(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="/anthropics">anthropics</a><script>{\"owners\":[{\"owner\":\"anthropics\",\"repos\":[{\"repo\":\"anthropics/skills\"}]}]}</script>`))
	})
	mux.HandleFunc("/repos/anthropics/skills", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "anthropics/skills", "stargazers_count": 50000, "default_branch": "main"})
	})
	mux.HandleFunc("/repos/anthropics/skills/git/trees/main", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []any{
			map[string]any{"path": "skills/frontend-design/SKILL.md", "type": "blob", "url": server.URL + "/blob/frontend-design"},
			map[string]any{"path": "plugins/document-skills/skills/pdf/SKILL.md", "type": "blob", "url": server.URL + "/blob/pdf"},
		}})
	})
	mux.HandleFunc("/search/repositories", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "items": []any{}})
	})
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "incomplete_results": false, "items": []any{}})
	})
	mux.HandleFunc("/blob/", func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# " + name))})
	})
	mux.HandleFunc("/repos/anthropics/skills/contents/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).WithConcurrency(4).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want every official repository skill: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Source != "anthropics/skills" || item.Stars != 50000 || !item.Official {
			t.Fatalf("official repository skill was classified incorrectly: %+v", item)
		}
	}
}

func TestDiscoverFetchesCandidatesConcurrently(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	var active, maximum atomic.Int32
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if r.URL.Query().Get("q") == "filename:SKILL.md" {
			for position := 0; position < 4; position++ {
				items = append(items, map[string]any{
					"name": "SKILL.md", "path": fmt.Sprintf("skills/demo-%d/SKILL.md", position), "url": fmt.Sprintf("%s/file/%d", server.URL, position),
					"repository": map[string]any{"full_name": fmt.Sprintf("acme/tool-%d", position), "stargazers_count": 11},
				})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
	})
	mux.HandleFunc("/file/", func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# Demo"))})
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).WithConcurrency(4).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	if maximum.Load() < 2 {
		t.Fatalf("maximum concurrent candidate fetches = %d, want at least 2", maximum.Load())
	}
}

func TestDiscoverSkipsLowStarRepositoryBeforeDownloadingContent(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	var contentRequests atomic.Int32
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if r.URL.Query().Get("q") == "filename:SKILL.md" {
			items = append(items, map[string]any{
				"name": "SKILL.md", "path": "skills/demo/SKILL.md", "url": server.URL + "/file",
				"repository": map[string]any{"full_name": "acme/tiny", "stargazers_count": 10},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		contentRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# Demo"))})
	})

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
	if contentRequests.Load() != 0 {
		t.Fatalf("content requests = %d, want 0", contentRequests.Load())
	}
}

func TestDiscoverSkippingDoesNotDownloadFreshSkill(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	var contentRequests atomic.Int32
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if r.URL.Query().Get("q") == "filename:SKILL.md" {
			items = append(items, map[string]any{
				"name": "SKILL.md", "path": "skills/demo/SKILL.md", "url": server.URL + "/file",
				"repository": map[string]any{"full_name": "acme/tool", "stargazers_count": 20},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		contentRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# Demo"))})
	})

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).DiscoverSkipping(context.Background(), map[string]struct{}{"acme/tool/demo": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "acme/tool/demo" || !items[0].Fresh {
		t.Fatalf("unexpected items: %+v", items)
	}
	if contentRequests.Load() != 0 {
		t.Fatalf("content requests = %d, want 0", contentRequests.Load())
	}
}

func TestDiscoverIgnoresFilenameNearMatches(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	var nearMatchRequests atomic.Int32
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if r.URL.Query().Get("q") == "filename:SKILLS.md" {
			items = append(items,
				map[string]any{"name": "hard-skills.md", "path": "docs/hard-skills.md", "url": server.URL + "/near-match", "repository": map[string]any{"full_name": "acme/docs", "stargazers_count": 20}},
				map[string]any{"name": "SKILLS.md", "path": "skills/demo/SKILLS.md", "url": server.URL + "/exact", "repository": map[string]any{"full_name": "acme/tool", "stargazers_count": 20}},
			)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
	})
	mux.HandleFunc("/near-match", func(w http.ResponseWriter, _ *http.Request) {
		nearMatchRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# Wrong"))})
	})
	mux.HandleFunc("/exact", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("# Demo"))})
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })

	items, err := NewClient(server.URL, "token", server.URL+"/official", server.Client()).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "acme/tool/demo" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if nearMatchRequests.Load() != 0 {
		t.Fatalf("near-match content requests = %d, want 0", nearMatchRequests.Load())
	}
}

func TestGetJSONWaitsAndRetriesGitHubRateLimits(t *testing.T) {
	tests := []struct {
		name            string
		token           string
		status          int
		headers         map[string]string
		wantAuth        bool
		wantWaitAtLeast time.Duration
	}{
		{name: "authenticated primary", token: "github-token", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10), "X-RateLimit-Resource": "core"}, wantAuth: true, wantWaitAtLeast: 50 * time.Second},
		{name: "unauthenticated primary", status: http.StatusTooManyRequests, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10), "X-RateLimit-Resource": "core"}, wantWaitAtLeast: 50 * time.Second},
		{name: "secondary", token: "github-token", status: http.StatusForbidden, headers: map[string]string{"Retry-After": "60", "X-RateLimit-Remaining": "42", "X-RateLimit-Resource": "core"}, wantAuth: true, wantWaitAtLeast: 59 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				if requests.Add(1) == 1 {
					for name, value := range test.headers {
						w.Header().Set(name, value)
					}
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer server.Close()

			client := NewClient(server.URL, test.token, server.URL, server.Client())
			var waited time.Duration
			client.wait = func(_ context.Context, delay time.Duration) error {
				waited += delay
				return nil
			}
			var response map[string]bool
			if err := client.getJSON(context.Background(), server.URL+"/repos/acme/tool", &response); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || !response["ok"] {
				t.Fatalf("requests = %d, response = %+v", requests.Load(), response)
			}
			if waited < test.wantWaitAtLeast {
				t.Fatalf("waited = %v, want at least %v", waited, test.wantWaitAtLeast)
			}
			if got := authorization != ""; got != test.wantAuth {
				t.Fatalf("Authorization present = %v, want %v", got, test.wantAuth)
			}
		})
	}
}

func TestGetJSONWaitsBeforeRequestAfterSuccessfulQuotaExhaustion(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
			w.Header().Set("X-RateLimit-Resource", "core")
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()

	client := NewClient(server.URL, "github-token", server.URL, server.Client())
	var waited time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		waited += delay
		return nil
	}
	for range 2 {
		var response map[string]bool
		if err := client.getJSON(context.Background(), server.URL+"/repos/acme/tool", &response); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if waited < 50*time.Second {
		t.Fatalf("waited = %v, want at least 50s", waited)
	}
}

func TestGetJSONReturnsTypedErrorAfterSecondaryRetryLimit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"secondary rate limit exceeded"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "github-token", server.URL, server.Client())
	client.wait = func(context.Context, time.Duration) error { return nil }
	var response map[string]bool
	err := client.getJSON(context.Background(), server.URL+"/repos/acme/tool", &response)
	var rateErr *rateLimitError
	if !errors.As(err, &rateErr) {
		t.Fatalf("error = %v, want rateLimitError", err)
	}
	if requests.Load() != maxRateLimitRetries+1 {
		t.Fatalf("requests = %d, want %d", requests.Load(), maxRateLimitRetries+1)
	}
}

func TestDiscoverFailsInsteadOfSkippingCandidateAfterRateLimitRetries(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/official", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<a href="/acme">acme</a>`)) })
	mux.HandleFunc("/search/code", func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		if r.URL.Query().Get("q") == "filename:SKILL.md" {
			items = append(items, map[string]any{"name": "SKILL.md", "path": "skills/demo/SKILL.md", "url": server.URL + "/file", "repository": map[string]any{"full_name": "acme/tool", "stargazers_count": 20}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"secondary rate limit exceeded"}`))
	})

	client := NewClient(server.URL, "github-token", server.URL+"/official", server.Client())
	client.wait = func(context.Context, time.Duration) error { return nil }
	_, err := client.Discover(context.Background())
	var rateErr *rateLimitError
	if !errors.As(err, &rateErr) {
		t.Fatalf("error = %v, want rateLimitError", err)
	}
}

func TestMetadataBoundsUntrustedFields(t *testing.T) {
	name, description := metadata("---\nname: " + strings.Repeat("n", 300) + "\ndescription: " + strings.Repeat("d", 1200) + "\n---")
	if len(name) != 200 {
		t.Fatalf("name length = %d, want 200", len(name))
	}
	if len(description) != 1000 {
		t.Fatalf("description length = %d, want 1000", len(description))
	}
}

func TestSlugifyBoundsUntrustedNames(t *testing.T) {
	if slug := slugify(strings.Repeat("a", 200)); len(slug) != 100 {
		t.Fatalf("slug length = %d, want 100", len(slug))
	}
}
