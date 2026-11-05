package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"gotest.tools/v3/assert"
)

// waitTimeout bounds how long tests wait for something to happen in the background.
const waitTimeout = 5 * time.Second

// fakeLister returns canned results and records how it was used. It is safe for concurrent use.
type fakeLister struct {
	mu    sync.Mutex
	prs   []ghclient.PullRequest
	err   error
	calls int

	// started, if set, is notified without blocking each time a fetch begins.
	started chan struct{}
	// release, if set, holds every fetch until it is closed.
	release chan struct{}
}

func (f *fakeLister) ListReviewRequested(ctx context.Context) ([]ghclient.PullRequest, error) {
	f.mu.Lock()
	f.calls++
	prs, err := f.prs, f.err
	f.mu.Unlock()

	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return prs, err
}

func (f *fakeLister) setResult(prs []ghclient.PullRequest, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs, f.err = prs, err
}

// testNow is the fixed time used by the server under test.
func testNow() time.Time {
	return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
}

func newTestServer(t *testing.T, lister PullRequestLister, opts ...Option) *Server {
	t.Helper()
	opts = append([]Option{WithClock(testNow)}, opts...)
	srv, err := New(slog.New(slog.DiscardHandler), lister, opts...)
	assert.NilError(t, err)
	return srv
}

func newTestHandler(t *testing.T, srv *Server) http.Handler {
	t.Helper()
	handler, err := srv.Handler()
	assert.NilError(t, err)
	return handler
}

func do(t *testing.T, handler http.Handler, method, target, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// index returns the body of the index page.
func index(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := do(t, handler, http.MethodGet, "/", "localhost:8080")
	assert.Equal(t, rec.Code, http.StatusOK)
	return rec.Body.String()
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestServer_Index_RendersSortedRows(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{prs: []ghclient.PullRequest{
		{
			Repository: "acme/web", Number: 7, Title: "bump <b>left-pad</b>", URL: "https://github.com/acme/web/pull/7",
			CreatedAt: testNow().Add(-3 * 24 * time.Hour), UpdatedAt: testNow().Add(-time.Hour),
			ReviewDecision: ghclient.ReviewRequired, MergeState: ghclient.MergeStateBlocked, CI: ghclient.CheckFailure,
		},
		{
			Repository: "acme/api", Number: 12, Title: "bump lib", URL: "https://github.com/acme/api/pull/12",
			CreatedAt: testNow().Add(-2 * time.Hour), UpdatedAt: testNow().Add(-time.Hour),
			ReviewDecision: ghclient.ReviewApproved, MergeState: ghclient.MergeStateClean, CI: ghclient.CheckSuccess,
			AutoMerge: true, MergeQueue: &ghclient.MergeQueueEntry{State: "QUEUED", Position: 2},
		},
	}}
	srv := newTestServer(t, lister)
	assert.NilError(t, srv.Refresh(context.Background()))

	body := index(t, newTestHandler(t, srv))

	for _, want := range []string{
		"2 open", "Updated 12:00:00", `data-polling="false"`,
		`href="https://github.com/acme/api/pull/12"`, "#12", "bump lib", "2h",
		`<span class="owner">acme</span><span class="name">api</span>`,
		"Approved", "Ready", "Auto-merge", "In merge queue #2", "✓ Passing",
		"Review required", "Blocked", "✗ Failing", "3d",
	} {
		assert.Assert(t, strings.Contains(body, want), "body is missing %q:\n%s", want, body)
	}
	apiIndex, webIndex := strings.Index(body, "acme/api"), strings.Index(body, "acme/web")
	assert.Assert(t, apiIndex < webIndex, "rows must be sorted by repository")
	assert.Assert(t, !strings.Contains(body, "<b>left-pad</b>"), "titles must be HTML-escaped")
	assert.Assert(t, strings.Contains(body, "&lt;b&gt;left-pad&lt;/b&gt;"))
}

func TestServer_Index_ShowsDescriptionOnTitleHover(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{prs: []ghclient.PullRequest{
		{
			Repository: "acme/api", Number: 1, Title: "described", URL: "https://github.com/acme/api/pull/1",
			Description: "Bumps <script>lib</script>.\n\n\n\nRelease notes",
		},
		{Repository: "acme/web", Number: 2, Title: "undescribed", URL: "https://github.com/acme/web/pull/2"},
	}}
	srv := newTestServer(t, lister)
	assert.NilError(t, srv.Refresh(context.Background()))

	body := index(t, newTestHandler(t, srv))

	described := `<a href="https://github.com/acme/api/pull/1" title="Bumps &lt;script&gt;lib&lt;/script&gt;.` +
		"\n\nRelease notes\""
	assert.Assert(t, strings.Contains(body, described), "described link is missing its tooltip:\n%s", body)
	assert.Assert(t, !strings.Contains(body, "<script>lib"), "descriptions must be HTML-escaped")
	assert.Assert(
		t,
		strings.Contains(body, `<a href="https://github.com/acme/web/pull/2" target="_blank"`),
		"a pull request without a description must not get a tooltip:\n%s",
		body,
	)
}

func TestServer_Index_SortsByRequestedColumn(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{prs: []ghclient.PullRequest{
		{Repository: "acme/a", Number: 1, Title: "failing one", CI: ghclient.CheckFailure},
		{Repository: "acme/b", Number: 2, Title: "passing one", CI: ghclient.CheckSuccess},
		{Repository: "acme/c", Number: 3, Title: "running one", CI: ghclient.CheckPending},
	}}
	srv := newTestServer(t, lister)
	assert.NilError(t, srv.Refresh(context.Background()))
	handler := newTestHandler(t, srv)

	for _, tt := range []struct {
		target string
		want   []string
	}{
		{"/", []string{"failing one", "passing one", "running one"}},
		{"/?sort=ci&dir=asc", []string{"passing one", "running one", "failing one"}},
		{"/?sort=ci&dir=desc", []string{"failing one", "running one", "passing one"}},
		{"/?sort=bogus&dir=desc", []string{"failing one", "passing one", "running one"}},
	} {
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()
			rec := do(t, handler, http.MethodGet, tt.target, "localhost")
			assert.Equal(t, rec.Code, http.StatusOK)
			body := rec.Body.String()
			first, second, third := strings.Index(
				body,
				tt.want[0],
			), strings.Index(
				body,
				tt.want[1],
			), strings.Index(
				body,
				tt.want[2],
			)
			assert.Assert(t, first < second && second < third, "rows are not in order %v:\n%s", tt.want, body)
		})
	}
}

func TestServer_Index_MarksSortedColumn(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeLister{prs: []ghclient.PullRequest{{Repository: "acme/a", Number: 1}}})
	assert.NilError(t, srv.Refresh(context.Background()))
	handler := newTestHandler(t, srv)

	body := do(t, handler, http.MethodGet, "/?sort=ci&dir=asc", "localhost").Body.String()

	for _, want := range []string{
		`<th aria-sort="ascending"><a href="/?dir=desc&amp;sort=ci">CI ` +
			`<span class="arrow" aria-hidden="true">▲</span></a></th>`,
		`<th><a href="/">Repository</a></th>`,
		`<form method="post" action="/refresh?dir=asc&amp;sort=ci">`,
	} {
		assert.Assert(t, strings.Contains(body, want), "body is missing %q:\n%s", want, body)
	}
	assert.Equal(t, strings.Count(body, "aria-sort="), 1, "only the sorted column is marked")
}

func TestServer_Index_Empty(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeLister{})
	assert.NilError(t, srv.Refresh(context.Background()))

	body := index(t, newTestHandler(t, srv))

	assert.Assert(t, strings.Contains(body, "No Dependabot pull requests are waiting for your review."))
	assert.Assert(t, !strings.Contains(body, "<table"))
	assert.Assert(t, !strings.Contains(body, "Loading"))
}

func TestServer_Index_LoadingBeforeFirstFetch(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{}

	body := index(t, newTestHandler(t, newTestServer(t, lister)))

	assert.Assert(t, strings.Contains(body, "Loading pull requests from GitHub"))
	assert.Assert(t, strings.Contains(body, `data-polling="true"`), "the page must poll while it waits for data")
	assert.Assert(t, !strings.Contains(body, "No Dependabot pull requests are waiting"))
	assert.Assert(t, !strings.Contains(body, "<table"))
	assert.Equal(t, lister.calls, 0, "rendering the page must never fetch from GitHub")
}

func TestServer_Index_ErrorWithoutData(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeLister{err: errors.New("rate limited")})
	assert.ErrorContains(t, srv.Refresh(context.Background()), "rate limited")

	body := index(t, newTestHandler(t, srv))

	assert.Assert(t, strings.Contains(body, "Could not fetch pull requests from GitHub: rate limited"))
	assert.Assert(t, strings.Contains(body, `data-polling="false"`))
	assert.Assert(t, !strings.Contains(body, "Loading"))
	assert.Assert(t, !strings.Contains(body, "No Dependabot pull requests are waiting"))
}

func TestServer_Index_ErrorKeepsLastSuccessfulData(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{prs: []ghclient.PullRequest{{Repository: "acme/api", Number: 1, Title: "bump lib"}}}
	srv := newTestServer(t, lister)
	assert.NilError(t, srv.Refresh(context.Background()))
	lister.setResult(nil, errors.New("boom"))
	assert.ErrorContains(t, srv.Refresh(context.Background()), "boom")

	body := index(t, newTestHandler(t, srv))

	assert.Assert(t, strings.Contains(body, "bump lib"), "stale data must still be shown")
	assert.Assert(t, strings.Contains(body, "Could not fetch pull requests from GitHub: boom"))
}

func TestServer_Index_ShowsUpdatingWhileFetching(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{
		prs:     []ghclient.PullRequest{{Repository: "acme/api", Number: 1, Title: "bump lib"}},
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	srv := newTestServer(t, lister)
	handler := newTestHandler(t, srv)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Refresh(context.Background())
	}()
	waitFor(t, lister.started, "the fetch to start")

	body := index(t, handler)

	assert.Assert(t, strings.Contains(body, "Updating…"))
	assert.Assert(t, strings.Contains(body, `data-polling="true"`))

	close(lister.release)
	waitFor(t, done, "the fetch to finish")

	body = index(t, handler)

	assert.Assert(t, strings.Contains(body, "bump lib"))
	assert.Assert(t, !strings.Contains(body, "Updating…"))
	assert.Assert(t, strings.Contains(body, `data-polling="false"`))
}

func TestServer_RefreshEndpoint_QueuesOneRefresh(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeLister{})
	handler := newTestHandler(t, srv)

	// A second request must not block even though nothing is consuming the first.
	for range 2 {
		rec := do(t, handler, http.MethodPost, "/refresh", "localhost")
		assert.Equal(t, rec.Code, http.StatusSeeOther)
		assert.Equal(t, rec.Header().Get("Location"), "/")
	}

	assert.Equal(t, len(srv.refreshRequests), 1)
	assert.Assert(t, strings.Contains(index(t, handler), "Updating…"), "the page must show that a refresh is under way")
}

func TestServer_RefreshEndpoint_KeepsSortOrder(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, newTestServer(t, &fakeLister{}))

	for _, tt := range []struct {
		target string
		want   string
	}{
		{"/refresh?sort=ci&dir=desc", "/?dir=desc&sort=ci"},
		{"/refresh?sort=bogus", "/"},
		{"/refresh?sort=ci&dir=asc&next=https://evil.example.com", "/?dir=asc&sort=ci"},
	} {
		rec := do(t, handler, http.MethodPost, tt.target, "localhost")
		assert.Equal(t, rec.Code, http.StatusSeeOther, tt.target)
		assert.Equal(t, rec.Header().Get("Location"), tt.want, tt.target)
	}
}

func TestServer_RejectsNonLoopbackHost(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, newTestServer(t, &fakeLister{}))

	for _, tt := range []struct {
		host string
		want int
	}{
		{host: "localhost:8080", want: http.StatusOK},
		{host: "127.0.0.1:8080", want: http.StatusOK},
		{host: "[::1]:8080", want: http.StatusOK},
		{host: "localhost", want: http.StatusOK},
		{host: "evil.example.com", want: http.StatusForbidden},
		{host: "evil.example.com:8080", want: http.StatusForbidden},
		{host: "localhost.evil.example.com:8080", want: http.StatusForbidden},
		{host: "", want: http.StatusForbidden},
	} {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if got := do(t, handler, http.MethodGet, "/", tt.host).Code; got != tt.want {
				t.Errorf("GET / with Host %q = %d, want %d", tt.host, got, tt.want)
			}
		})
	}
}

func TestServer_ServesStaticFiles(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, newTestServer(t, &fakeLister{}))

	for _, path := range []string{"/static/style.css", "/static/app.js"} {
		rec := do(t, handler, http.MethodGet, path, "localhost")
		assert.Equal(t, rec.Code, http.StatusOK, path)
	}
}

func TestServer_UnknownPathIsNotFound(t *testing.T) {
	t.Parallel()

	rec := do(t, newTestHandler(t, newTestServer(t, &fakeLister{})), http.MethodGet, "/nope", "localhost")

	assert.Equal(t, rec.Code, http.StatusNotFound)
}

// startServer serves srv on a free loopback port. It returns the base URL and a function that stops the server and
// returns the result of Serve.
func startServer(t *testing.T, srv *Server) (string, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, listener) }()
	stop := func() error {
		cancel()
		select {
		case err := <-errCh:
			return err
		case <-time.After(waitTimeout):
			return errors.New("timed out waiting for Serve to return")
		}
	}
	return "http://" + listener.Addr().String(), stop
}

func request(t *testing.T, method, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	assert.NilError(t, err)
	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	assert.NilError(t, err)
	return string(body)
}

func TestServer_Serve_RefreshesInBackground(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{
		prs:     []ghclient.PullRequest{{Repository: "acme/api", Number: 1, Title: "bump lib"}},
		started: make(chan struct{}, 1),
	}
	srv := newTestServer(t, lister, WithRefreshInterval(time.Millisecond))
	baseURL, stop := startServer(t, srv)

	// The second fetch can only start after the first one has been recorded.
	waitFor(t, lister.started, "the first fetch")
	waitFor(t, lister.started, "the second fetch")

	assert.Assert(t, strings.Contains(request(t, http.MethodGet, baseURL+"/"), "bump lib"))
	assert.NilError(t, stop())
}

func TestServer_Serve_RefreshRequestTriggersFetch(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{started: make(chan struct{}, 1)}
	srv := newTestServer(t, lister, WithRefreshInterval(time.Hour))
	baseURL, stop := startServer(t, srv)
	waitFor(t, lister.started, "the first fetch")

	request(t, http.MethodPost, baseURL+"/refresh")

	waitFor(t, lister.started, "the requested fetch")
	assert.NilError(t, stop())
}

func TestServer_Serve_StopsWhileFetching(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{started: make(chan struct{}, 1), release: make(chan struct{})}
	srv := newTestServer(t, lister)
	_, stop := startServer(t, srv)
	waitFor(t, lister.started, "the first fetch")

	assert.NilError(t, stop(), "a fetch that is still running must be canceled on shutdown")
}
