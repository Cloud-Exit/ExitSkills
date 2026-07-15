package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/exitmesh/skills/internal/indexer"
	"github.com/exitmesh/skills/internal/model"
)

const maxSearchPages = 10
const maxPopularRepositoryPages = 10
const rankedSkillRepositoryQuery = "(SKILL.md OR SKILLS.md) in:readme stars:>10"
const maxSkillFiles = 64
const maxFileBytes = 512 << 10
const maxRateLimitRetries = 3

type rateGate struct {
	mu           sync.Mutex
	blockedUntil map[string]time.Time
}

type Client struct {
	baseURL, officialURL string
	authenticator        RequestAuthenticator
	http                 *http.Client
	logger               *slog.Logger
	concurrency          int
	rateLimits           *rateGate
	wait                 func(context.Context, time.Duration) error
}

func NewClient(baseURL, token, officialURL string, httpClient *http.Client) *Client {
	var authenticator RequestAuthenticator
	if token != "" {
		authenticator = NewTokenAuthenticator(token)
	}
	return NewClientWithAuthenticator(baseURL, authenticator, officialURL, httpClient)
}

func NewClientWithAuthenticator(baseURL string, authenticator RequestAuthenticator, officialURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), authenticator: authenticator, officialURL: officialURL, http: httpClient,
		logger: logger, concurrency: 8, rateLimits: &rateGate{blockedUntil: make(map[string]time.Time)}, wait: waitContext,
	}
}

func (c *Client) WithConcurrency(concurrency int) *Client {
	if concurrency > 0 {
		c.concurrency = concurrency
	}
	return c
}

func (c *Client) WithLogger(logger *slog.Logger) *Client {
	if logger != nil {
		c.logger = logger
	}
	return c
}

type searchResponse struct {
	TotalCount int          `json:"total_count"`
	Incomplete bool         `json:"incomplete_results"`
	Items      []searchItem `json:"items"`
}
type searchItem struct {
	Name, Path, URL string
	Repository      struct {
		FullName string `json:"full_name"`
		Stars    int    `json:"stargazers_count"`
	} `json:"repository"`
}
type contentItem struct {
	Type, Name, Path, URL, Encoding, Content string
	Size                                     int
}

type repositorySearchResponse struct {
	TotalCount int                `json:"total_count"`
	Items      []repositorySearch `json:"items"`
}

type repositorySearch struct {
	FullName      string `json:"full_name"`
	Stars         int    `json:"stargazers_count"`
	DefaultBranch string `json:"default_branch"`
}

type officialCatalog struct {
	Owners       map[string]bool
	Repositories []string
}

type treeResponse struct {
	Truncated bool       `json:"truncated"`
	Tree      []treeItem `json:"tree"`
}

type treeItem struct {
	Path, Type, URL string
}

func (c *Client) Discover(ctx context.Context) ([]indexer.Candidate, error) {
	return c.discover(ctx, nil)
}

func (c *Client) DiscoverSkipping(ctx context.Context, freshIDs map[string]struct{}) ([]indexer.Candidate, error) {
	return c.discover(ctx, freshIDs)
}

func (c *Client) discover(ctx context.Context, freshIDs map[string]struct{}) ([]indexer.Candidate, error) {
	c.logger.Info("official catalog loading")
	catalog, err := c.loadOfficialCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("load official skills: %w", err)
	}
	officialOwners := catalog.Owners
	c.logger.Info("official catalog loaded", "owners", len(officialOwners), "repositories", len(catalog.Repositories))
	seen := map[string]bool{}
	candidates := make([]indexer.Candidate, 0)
	duplicates, contentFailures, filenameMismatches, freshSkipped := 0, 0, 0, 0
	officialCandidates, officialDuplicates, officialFailures, officialFresh, err := c.discoverOfficialRepositories(ctx, catalog.Repositories, freshIDs, officialOwners, seen)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, officialCandidates...)
	duplicates += officialDuplicates
	contentFailures += officialFailures
	freshSkipped += officialFresh
	popularCandidates, popularDuplicates, popularFailures, popularFresh, err := c.discoverPopularRepositories(ctx, freshIDs, officialOwners, seen)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, popularCandidates...)
	duplicates += popularDuplicates
	contentFailures += popularFailures
	freshSkipped += popularFresh
	for _, filename := range []string{"SKILL.md", "SKILLS.md"} {
		c.logger.Info("github skill search started", "filename", filename)
		for pageNumber := 1; pageNumber <= maxSearchPages; pageNumber++ {
			var result searchResponse
			query := url.Values{"q": {"filename:" + filename}, "per_page": {"100"}, "page": {fmt.Sprint(pageNumber)}}
			if err := c.getJSON(ctx, c.baseURL+"/search/code?"+query.Encode(), &result); err != nil {
				return nil, fmt.Errorf("search GitHub code: %w", err)
			}
			c.logger.Info("github skill search page", "filename", filename, "page", pageNumber, "results", len(result.Items), "total", result.TotalCount, "incomplete", result.Incomplete)
			candidatesBeforePage := len(candidates)
			pageItems := make([]searchItem, 0, len(result.Items))
			for _, item := range result.Items {
				if path.Base(item.Path) != filename {
					filenameMismatches++
					c.logger.Debug("search result skipped", "repository", item.Repository.FullName, "path", item.Path, "reason", "filename_mismatch", "expected", filename)
					continue
				}
				folder, slug, id, owner := candidateIdentity(item)
				key := id
				if seen[key] {
					duplicates++
					continue
				}
				seen[key] = true
				if _, fresh := freshIDs[id]; fresh {
					freshSkipped++
					candidates = append(candidates, indexer.Candidate{ID: id, Source: item.Repository.FullName, Slug: slug, Stars: item.Repository.Stars, Official: officialOwners[strings.ToLower(owner)], Fresh: true})
					c.logger.Debug("skill skipped before content download", "skill_id", id, "path", folder, "reason", "fresh")
					continue
				}
				pageItems = append(pageItems, item)
			}
			pageCandidates, failures, err := c.fetchCandidates(ctx, pageItems, officialOwners, filename, pageNumber)
			if err != nil {
				return nil, err
			}
			contentFailures += failures
			candidates = append(candidates, pageCandidates...)
			c.logger.Info("github skill search page processed", "filename", filename, "page", pageNumber, "new_candidates", len(candidates)-candidatesBeforePage, "candidates", len(candidates), "skipped_fresh", freshSkipped, "filename_mismatches", filenameMismatches, "duplicates", duplicates, "content_failures", contentFailures)
			if len(result.Items) < 100 || pageNumber*100 >= result.TotalCount {
				break
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	official := 0
	for _, candidate := range candidates {
		if candidate.Official {
			official++
		}
	}
	c.logger.Info("github skill discovery complete", "candidates", len(candidates), "official", official, "skipped_fresh", freshSkipped, "filename_mismatches", filenameMismatches, "duplicates", duplicates, "content_failures", contentFailures)
	return candidates, nil
}

type repositoryMetadataResult struct {
	repository repositorySearch
	failed     bool
	lowStars   bool
	err        error
}

func (c *Client) discoverOfficialRepositories(ctx context.Context, names []string, freshIDs map[string]struct{}, officialOwners map[string]bool, seen map[string]bool) ([]indexer.Candidate, int, int, int, error) {
	if len(names) == 0 {
		c.logger.Warn("official catalog contained no repository identities; continuing with ranked and code search")
		return nil, 0, 0, 0, nil
	}
	c.logger.Info("official repository discovery started", "repositories", len(names), "minimum_stars", 11)
	repositories, metadataFailures, lowStars, err := c.fetchRepositoryMetadata(ctx, names)
	if err != nil {
		return nil, 0, metadataFailures, 0, err
	}
	c.logger.Info("official repository metadata loaded", "eligible", len(repositories), "skipped_stars", lowStars, "failures", metadataFailures)
	items, treeFailures, err := c.fetchRepositorySkillItems(ctx, repositories, "official", 1)
	if err != nil {
		return nil, 0, metadataFailures + treeFailures, 0, err
	}
	duplicates, freshSkipped := 0, 0
	pageItems := make([]searchItem, 0, len(items))
	candidates := make([]indexer.Candidate, 0, len(items))
	for _, item := range items {
		folder, slug, id, owner := candidateIdentity(item)
		if seen[id] {
			duplicates++
			continue
		}
		seen[id] = true
		if _, fresh := freshIDs[id]; fresh {
			freshSkipped++
			candidates = append(candidates, indexer.Candidate{ID: id, Source: item.Repository.FullName, Slug: slug, Stars: item.Repository.Stars, Official: officialOwners[strings.ToLower(owner)], Fresh: true})
			c.logger.Debug("skill skipped before content download", "skill_id", id, "path", folder, "reason", "fresh")
			continue
		}
		pageItems = append(pageItems, item)
	}
	pageCandidates, contentFailures, err := c.fetchCandidates(ctx, pageItems, officialOwners, "official", 1)
	if err != nil {
		return nil, duplicates, metadataFailures + treeFailures + contentFailures, freshSkipped, err
	}
	candidates = append(candidates, pageCandidates...)
	failures := metadataFailures + treeFailures + contentFailures
	c.logger.Info("official repository discovery complete", "repositories", len(repositories), "skill_files", len(items), "candidates", len(candidates), "skipped_stars", lowStars, "skipped_fresh", freshSkipped, "duplicates", duplicates, "failures", failures)
	return candidates, duplicates, failures, freshSkipped, nil
}

func (c *Client) fetchRepositoryMetadata(ctx context.Context, names []string) ([]repositorySearch, int, int, error) {
	workers := min(c.concurrency, len(names))
	jobs := make(chan string, len(names))
	results := make(chan repositoryMetadataResult, len(names))
	for _, name := range names {
		jobs <- name
	}
	close(jobs)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for name := range jobs {
				var repository repositorySearch
				if err := c.getJSON(ctx, c.baseURL+"/repos/"+name, &repository); err != nil {
					results <- repositoryMetadataResult{failed: true, err: err}
					continue
				}
				if repository.FullName == "" {
					repository.FullName = name
				}
				if repository.Stars <= 10 {
					results <- repositoryMetadataResult{lowStars: true}
					continue
				}
				if repository.DefaultBranch == "" {
					results <- repositoryMetadataResult{failed: true}
					continue
				}
				results <- repositoryMetadataResult{repository: repository}
			}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()
	repositories := make([]repositorySearch, 0, len(names))
	failures, lowStars, processed := 0, 0, 0
	for result := range results {
		processed++
		if result.err != nil && requestMustAbort(ctx, result.err) {
			return nil, failures, lowStars, result.err
		}
		switch {
		case result.failed:
			failures++
		case result.lowStars:
			lowStars++
		default:
			repositories = append(repositories, result.repository)
		}
		if processed%25 == 0 || processed == len(names) {
			c.logger.Info("official repository metadata progress", "processed", processed, "total", len(names), "eligible", len(repositories), "skipped_stars", lowStars, "failures", failures)
		}
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].FullName < repositories[j].FullName })
	return repositories, failures, lowStars, nil
}

func (c *Client) discoverPopularRepositories(ctx context.Context, freshIDs map[string]struct{}, officialOwners map[string]bool, seen map[string]bool) ([]indexer.Candidate, int, int, int, error) {
	c.logger.Info("popular repository discovery started", "query", rankedSkillRepositoryQuery, "minimum_stars", 11, "max_pages", maxPopularRepositoryPages)
	candidates := make([]indexer.Candidate, 0)
	duplicates, contentFailures, freshSkipped := 0, 0, 0
	for pageNumber := 1; pageNumber <= maxPopularRepositoryPages; pageNumber++ {
		var result repositorySearchResponse
		query := url.Values{"q": {rankedSkillRepositoryQuery}, "sort": {"stars"}, "order": {"desc"}, "per_page": {"100"}, "page": {fmt.Sprint(pageNumber)}}
		if err := c.getJSON(ctx, c.baseURL+"/search/repositories?"+query.Encode(), &result); err != nil {
			if requestMustAbort(ctx, err) {
				return nil, duplicates, contentFailures, freshSkipped, err
			}
			c.logger.Warn("popular repository discovery unavailable; continuing with code search", "page", pageNumber, "error", err)
			break
		}
		c.logger.Info("popular repository search page", "page", pageNumber, "repositories", len(result.Items), "total", result.TotalCount)
		items, treeFailures, err := c.fetchRepositorySkillItems(ctx, result.Items, "popular", pageNumber)
		if err != nil {
			return nil, duplicates, contentFailures, freshSkipped, err
		}
		contentFailures += treeFailures
		pageItems := make([]searchItem, 0, len(items))
		for _, item := range items {
			folder, slug, id, owner := candidateIdentity(item)
			if seen[id] {
				duplicates++
				continue
			}
			seen[id] = true
			if _, fresh := freshIDs[id]; fresh {
				freshSkipped++
				candidates = append(candidates, indexer.Candidate{ID: id, Source: item.Repository.FullName, Slug: slug, Stars: item.Repository.Stars, Official: officialOwners[strings.ToLower(owner)], Fresh: true})
				c.logger.Debug("skill skipped before content download", "skill_id", id, "path", folder, "reason", "fresh")
				continue
			}
			pageItems = append(pageItems, item)
		}
		pageCandidates, failures, err := c.fetchCandidates(ctx, pageItems, officialOwners, "popular", pageNumber)
		if err != nil {
			return nil, duplicates, contentFailures, freshSkipped, err
		}
		contentFailures += failures
		candidates = append(candidates, pageCandidates...)
		c.logger.Info("popular repository page processed", "page", pageNumber, "skill_files", len(items), "new_candidates", len(pageCandidates), "candidates", len(candidates), "tree_failures", treeFailures, "content_failures", contentFailures, "duplicates", duplicates, "skipped_fresh", freshSkipped)
		if len(result.Items) < 100 || pageNumber*100 >= result.TotalCount {
			break
		}
	}
	c.logger.Info("popular repository discovery complete", "candidates", len(candidates), "duplicates", duplicates, "content_failures", contentFailures, "skipped_fresh", freshSkipped)
	return candidates, duplicates, contentFailures, freshSkipped, nil
}

type repositoryTreeResult struct {
	items     []searchItem
	failed    bool
	err       error
	truncated bool
}

func (c *Client) fetchRepositorySkillItems(ctx context.Context, repositories []repositorySearch, source string, page int) ([]searchItem, int, error) {
	if len(repositories) == 0 {
		return nil, 0, nil
	}
	workers := min(c.concurrency, len(repositories))
	jobs := make(chan repositorySearch, len(repositories))
	results := make(chan repositoryTreeResult, len(repositories))
	for _, repository := range repositories {
		jobs <- repository
	}
	close(jobs)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for repository := range jobs {
				var tree treeResponse
				endpoint := c.baseURL + "/repos/" + repository.FullName + "/git/trees/" + url.PathEscape(repository.DefaultBranch) + "?recursive=1"
				if err := c.getJSON(ctx, endpoint, &tree); err != nil {
					results <- repositoryTreeResult{failed: true, err: err}
					continue
				}
				sort.Slice(tree.Tree, func(i, j int) bool {
					leftDepth, rightDepth := strings.Count(tree.Tree[i].Path, "/"), strings.Count(tree.Tree[j].Path, "/")
					if leftDepth != rightDepth {
						return leftDepth < rightDepth
					}
					return tree.Tree[i].Path < tree.Tree[j].Path
				})
				items := make([]searchItem, 0)
				for _, entry := range tree.Tree {
					name := path.Base(entry.Path)
					if entry.Type != "blob" || (name != "SKILL.md" && name != "SKILLS.md") {
						continue
					}
					var item searchItem
					item.Name, item.Path, item.URL = name, entry.Path, entry.URL
					item.Repository.FullName, item.Repository.Stars = repository.FullName, repository.Stars
					items = append(items, item)
				}
				results <- repositoryTreeResult{items: items, truncated: tree.Truncated}
			}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()
	items := make([]searchItem, 0)
	failures, processed, truncated := 0, 0, 0
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case result, open := <-results:
			if !open {
				return items, failures, nil
			}
			processed++
			if result.err != nil && requestMustAbort(ctx, result.err) {
				return nil, failures, result.err
			}
			if result.failed {
				failures++
			} else {
				items = append(items, result.items...)
			}
			if result.truncated {
				truncated++
			}
			if processed%10 == 0 || processed == len(repositories) {
				c.logger.Info(source+" repository tree progress", "page", page, "processed", processed, "total", len(repositories), "skill_files", len(items), "failures", failures, "truncated", truncated)
			}
		case <-heartbeat.C:
			c.logger.Info(source+" repository trees still processing", "page", page, "processed", processed, "total", len(repositories), "skill_files", len(items), "failures", failures, "truncated", truncated)
		}
	}
}

type candidateResult struct {
	candidate     indexer.Candidate
	found         bool
	contentFailed bool
	starsSkipped  bool
	err           error
}

func (c *Client) fetchCandidates(ctx context.Context, items []searchItem, officialOwners map[string]bool, filename string, page int) ([]indexer.Candidate, int, error) {
	if len(items) == 0 {
		return nil, 0, nil
	}
	workers := min(c.concurrency, len(items))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan searchItem, len(items))
	results := make(chan candidateResult, len(items))
	for _, item := range items {
		jobs <- item
	}
	close(jobs)

	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for item := range jobs {
				if workCtx.Err() != nil {
					return
				}
				result := c.fetchCandidate(workCtx, item, officialOwners)
				results <- result
				if result.err != nil {
					cancel()
					return
				}
			}
		}()
	}
	c.logger.Info("github skill page fetch started", "filename", filename, "page", page, "items", len(items), "concurrency", workers)
	go func() {
		group.Wait()
		close(results)
	}()

	candidates := make([]indexer.Candidate, 0, len(items))
	contentFailures := 0
	starsSkipped := 0
	processed := 0
	var fetchErr error
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case result, open := <-results:
			if !open {
				if fetchErr != nil {
					return nil, contentFailures, fetchErr
				}
				if err := ctx.Err(); err != nil {
					return nil, contentFailures, err
				}
				return candidates, contentFailures, nil
			}
			processed++
			if result.err != nil && fetchErr == nil {
				fetchErr = result.err
			}
			if result.contentFailed {
				contentFailures++
			}
			if result.starsSkipped {
				starsSkipped++
			}
			if result.found {
				candidates = append(candidates, result.candidate)
			}
			if processed%10 == 0 || processed == len(items) {
				c.logger.Info("github skill page progress", "filename", filename, "page", page, "processed", processed, "total", len(items), "candidates_found", len(candidates), "skipped_stars", starsSkipped, "content_failures", contentFailures)
			}
		case <-heartbeat.C:
			c.logger.Info("github skill page still processing", "filename", filename, "page", page, "processed", processed, "total", len(items), "candidates_found", len(candidates), "skipped_stars", starsSkipped, "content_failures", contentFailures)
		}
	}
}

func (c *Client) fetchCandidate(ctx context.Context, item searchItem, officialOwners map[string]bool) candidateResult {
	stars := item.Repository.Stars
	if stars == 0 {
		var err error
		stars, err = c.repoStars(ctx, item.Repository.FullName)
		if err != nil {
			if requestMustAbort(ctx, err) {
				return candidateResult{err: err}
			}
			c.logger.Debug("github repository stars unavailable", "repository", item.Repository.FullName, "error", err)
		}
	}
	if stars <= 10 {
		c.logger.Debug("skill skipped", "repository", item.Repository.FullName, "path", item.Path, "reason", "stars", "stars", stars)
		return candidateResult{starsSkipped: true}
	}
	file, err := c.getContent(ctx, item.URL)
	if err != nil {
		if requestMustAbort(ctx, err) {
			return candidateResult{err: err}
		}
		c.logger.Debug("skill file skipped", "repository", item.Repository.FullName, "path", item.Path, "reason", "content_unavailable", "error", err)
		return candidateResult{contentFailed: true}
	}
	folder, slug, id, owner := candidateIdentity(item)
	files, err := c.folderFiles(ctx, item.Repository.FullName, folder)
	if err != nil && requestMustAbort(ctx, err) {
		return candidateResult{err: err}
	}
	if err != nil || len(files) == 0 {
		c.logger.Debug("skill supporting files unavailable", "repository", item.Repository.FullName, "path", folder, "fallback", "skill_file_only")
		files = []model.File{{Path: path.Base(item.Path), Contents: file}}
	}
	name, description := metadata(file)
	slugBase := path.Base(folder)
	if folder == "." {
		_, slugBase = sourceParts(item.Repository.FullName)
	}
	if name == "" {
		name = slugBase
	}
	candidate := indexer.Candidate{ID: id, Source: item.Repository.FullName, Slug: slug, Name: name, Description: description, Contents: file, Stars: stars, Official: officialOwners[strings.ToLower(owner)], Files: files}
	c.logger.Debug("skill candidate discovered", "skill_id", candidate.ID, "stars", stars, "official", candidate.Official, "files", len(files))
	return candidateResult{candidate: candidate, found: true}
}

func candidateIdentity(item searchItem) (folder, slug, id, owner string) {
	folder = path.Dir(item.Path)
	slugBase := path.Base(folder)
	if folder == "." {
		_, slugBase = sourceParts(item.Repository.FullName)
	}
	slug = slugify(slugBase)
	owner, _ = sourceParts(item.Repository.FullName)
	id = item.Repository.FullName + "/" + slug
	return folder, slug, id, owner
}

func (c *Client) repoStars(ctx context.Context, repo string) (int, error) {
	var data struct {
		Stars int `json:"stargazers_count"`
	}
	err := c.getJSON(ctx, c.baseURL+"/repos/"+repo, &data)
	return data.Stars, err
}
func (c *Client) getContent(ctx context.Context, endpoint string) (string, error) {
	var item contentItem
	if err := c.getJSON(ctx, endpoint, &item); err != nil {
		return "", err
	}
	if item.Encoding != "base64" {
		return "", fmt.Errorf("unsupported GitHub content encoding %q", item.Encoding)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(item.Content, "\n", ""))
	if err != nil {
		return "", err
	}
	if len(raw) > maxFileBytes {
		return "", fmt.Errorf("file exceeds %d bytes", maxFileBytes)
	}
	return string(raw), nil
}

func (c *Client) folderFiles(ctx context.Context, repo, folder string) ([]model.File, error) {
	endpoint := c.baseURL + "/repos/" + repo + "/contents"
	if folder != "." {
		endpoint += "/" + strings.TrimPrefix(folder, "/")
	}
	return c.walkFolder(ctx, endpoint, folder, 0)
}
func (c *Client) walkFolder(ctx context.Context, endpoint, root string, depth int) ([]model.File, error) {
	if depth > 4 {
		return nil, nil
	}
	var items []contentItem
	if err := c.getJSON(ctx, endpoint, &items); err != nil {
		return nil, err
	}
	files := make([]model.File, 0)
	for _, item := range items {
		if len(files) >= maxSkillFiles {
			break
		}
		switch item.Type {
		case "file":
			if item.Size > maxFileBytes {
				continue
			}
			contents, err := c.getContent(ctx, item.URL)
			if err != nil {
				if requestMustAbort(ctx, err) {
					return nil, err
				}
				continue
			}
			relative := strings.TrimPrefix(strings.TrimPrefix(item.Path, root), "/")
			files = append(files, model.File{Path: relative, Contents: contents})
		case "dir":
			nested, err := c.walkFolder(ctx, item.URL, root, depth+1)
			if err != nil && requestMustAbort(ctx, err) {
				return nil, err
			}
			if err == nil {
				files = append(files, nested...)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) > maxSkillFiles {
		files = files[:maxSkillFiles]
	}
	return files, nil
}

func (c *Client) loadOfficialCatalog(ctx context.Context) (officialCatalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.officialURL, nil)
	if err != nil {
		return officialCatalog{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return officialCatalog{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return officialCatalog{}, fmt.Errorf("official page returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return officialCatalog{}, err
	}
	owners := map[string]bool{}
	for _, match := range regexp.MustCompile(`href=["']/([A-Za-z0-9][A-Za-z0-9-]{0,38})["']`).FindAllSubmatch(body, -1) {
		owner := strings.ToLower(string(match[1]))
		if !reservedOwner(owner) {
			owners[owner] = true
		}
	}
	repositories, seen := make([]string, 0), map[string]bool{}
	for _, match := range regexp.MustCompile(`(?:\\?")repo(?:\\?")\s*:\s*(?:\\?")([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:\\?")`).FindAllSubmatch(body, -1) {
		repository := string(match[1])
		key := strings.ToLower(repository)
		if seen[key] {
			continue
		}
		seen[key] = true
		repositories = append(repositories, repository)
		owner, _ := sourceParts(repository)
		owners[strings.ToLower(owner)] = true
	}
	if len(owners) == 0 {
		return officialCatalog{}, fmt.Errorf("official page contained no creator links")
	}
	sort.Strings(repositories)
	return officialCatalog{Owners: owners, Repositories: repositories}, nil
}
func reservedOwner(value string) bool {
	switch value {
	case "official", "docs", "topics", "api", "audits", "trending", "hot", "about", "privacy", "terms":
		return true
	}
	return false
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	resource := rateLimitResource(endpoint)
	for attempt := 0; ; attempt++ {
		if err := c.awaitRateLimit(ctx, resource); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.authenticator != nil {
			if err := c.authenticator.Authenticate(ctx, req); err != nil {
				return fmt.Errorf("authenticate GitHub request: %w", err)
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.observeRateLimit(resource, resp.Header)
			err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target)
			_ = resp.Body.Close()
			return err
		}

		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = resp.Body.Close()
		responseErr := fmt.Errorf("GET %s returned %s: %s", endpoint, resp.Status, strings.TrimSpace(string(message)))
		until, limited := c.rateLimitReset(resp.StatusCode, resp.Header, message, attempt)
		if !limited {
			return responseErr
		}
		if attempt >= maxRateLimitRetries {
			return &rateLimitError{cause: responseErr}
		}
		c.blockRateLimit(resource, until)
		authentication := c.authenticationName()
		responseResource := resp.Header.Get("X-RateLimit-Resource")
		if responseResource == "" {
			responseResource = resource
		}
		c.logger.Warn("github rate limit reached; request will retry", "authentication", authentication, "resource", responseResource, "limit", resp.Header.Get("X-RateLimit-Limit"), "remaining", resp.Header.Get("X-RateLimit-Remaining"), "reset_at", until.UTC(), "retry_in", time.Until(until).Round(time.Second), "attempt", attempt+1)
	}
}

type rateLimitError struct{ cause error }

func (e *rateLimitError) Error() string { return e.cause.Error() }
func (e *rateLimitError) Unwrap() error { return e.cause }

func requestMustAbort(ctx context.Context, err error) bool {
	var rateErr *rateLimitError
	return ctx.Err() != nil || errors.As(err, &rateErr)
}

func rateLimitResource(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Path == "/search/code" {
		return "code_search"
	}
	return "core"
}

func (c *Client) awaitRateLimit(ctx context.Context, resource string) error {
	c.rateLimits.mu.Lock()
	defer c.rateLimits.mu.Unlock()
	until := c.rateLimits.blockedUntil[resource]
	delay := time.Until(until)
	if delay <= 0 {
		return ctx.Err()
	}
	c.logger.Info("github request waiting for rate limit reset", "resource", resource, "reset_at", until.UTC(), "wait", delay.Round(time.Second))
	return c.wait(ctx, delay)
}

func (c *Client) blockRateLimit(resource string, until time.Time) {
	c.rateLimits.mu.Lock()
	defer c.rateLimits.mu.Unlock()
	if until.After(c.rateLimits.blockedUntil[resource]) {
		c.rateLimits.blockedUntil[resource] = until
	}
}

func (c *Client) observeRateLimit(resource string, headers http.Header) {
	remaining, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	if remaining == 0 {
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			c.blockRateLimit(resource, time.Unix(reset, 0))
		}
	}
	limit, _ := strconv.Atoi(headers.Get("X-RateLimit-Limit"))
	lowWatermark := remaining <= 1
	if limit > 0 {
		lowWatermark = lowWatermark || remaining == limit/10 || remaining == limit/20
	}
	if lowWatermark {
		authentication := c.authenticationName()
		c.logger.Warn("github rate limit running low", "authentication", authentication, "resource", resource, "remaining", remaining, "limit", headers.Get("X-RateLimit-Limit"), "reset", headers.Get("X-RateLimit-Reset"))
	}
}

func (c *Client) authenticationName() string {
	if c.authenticator == nil {
		return "unauthenticated"
	}
	return c.authenticator.Name()
}

func (c *Client) rateLimitReset(status int, headers http.Header, message []byte, attempt int) (time.Time, bool) {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	lowerMessage := strings.ToLower(string(message))
	limited := status == http.StatusTooManyRequests || headers.Get("Retry-After") != "" || headers.Get("X-RateLimit-Remaining") == "0" || strings.Contains(lowerMessage, "rate limit") || strings.Contains(lowerMessage, "secondary limit")
	if !limited {
		return time.Time{}, false
	}
	now := time.Now()
	until := time.Time{}
	if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			until = now.Add(time.Duration(seconds) * time.Second * time.Duration(1<<min(attempt, 4)))
		} else if date, err := http.ParseTime(retryAfter); err == nil {
			until = date
		}
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			resetAt := time.Unix(reset, 0)
			if resetAt.After(until) {
				until = resetAt
			}
		}
	}
	if until.IsZero() || until.Before(now) {
		until = now.Add(time.Minute * time.Duration(1<<min(attempt, 4)))
	}
	return until, true
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func metadata(contents string) (string, string) {
	name, description := "", ""
	lines := strings.Split(contents, "\n")
	inFrontmatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i > 0 && inFrontmatter && trimmed == "---" {
			inFrontmatter = false
			continue
		}
		if inFrontmatter {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
				switch strings.ToLower(parts[0]) {
				case "name":
					name = value
				case "description":
					description = value
				}
			}
		} else if name == "" && strings.HasPrefix(trimmed, "# ") {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return truncateRunes(name, 200), truncateRunes(description, 1000)
}
func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	dash := false
	for _, r := range value {
		if out.Len() >= 100 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			dash = false
		} else if !dash && out.Len() > 0 {
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
func sourceParts(source string) (string, string) {
	parts := strings.SplitN(source, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return source, source
}
