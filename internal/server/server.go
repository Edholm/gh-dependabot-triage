// Package server serves a local web page listing the Dependabot pull requests awaiting review.
//
// Fetching pull requests from GitHub is slow, so the server never does it while handling a request. A background
// loop refreshes the list and requests only render the most recent result.
package server

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

//go:embed templates/index.html.tmpl
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	// DefaultRefreshInterval is how long the server waits between fetching pull requests from GitHub.
	DefaultRefreshInterval = time.Minute

	// fetchTimeout bounds a single fetch of all pull requests.
	fetchTimeout      = 2 * time.Minute
	shutdownTimeout   = 5 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// PullRequestLister lists the pull requests to show.
type PullRequestLister interface {
	ListReviewRequested(ctx context.Context) ([]ghclient.PullRequest, error)
}

// Server serves the pull request page.
type Server struct {
	logger          *slog.Logger
	lister          PullRequestLister
	template        *template.Template
	now             func() time.Time
	refreshInterval time.Duration
	// refreshRequests asks the refresh loop to fetch right away. It has room for one pending request.
	refreshRequests chan struct{}

	queue *queue

	mu    sync.Mutex
	state state
}

// Option configures a Server.
type Option func(*Server)

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// WithRefreshInterval overrides how long the server waits between fetching pull requests from GitHub.
func WithRefreshInterval(interval time.Duration) Option {
	return func(s *Server) { s.refreshInterval = interval }
}

// WithQueueTiming overrides how often the merge queue checks a pull request it is waiting for, and how long it waits
// for it to become mergeable.
func WithQueueTiming(pollInterval, waitTimeout time.Duration) Option {
	return func(s *Server) {
		s.queue.pollInterval = pollInterval
		s.queue.waitTimeout = waitTimeout
	}
}

// New returns a Server that shows the pull requests returned by lister, and approves and merges them with merger.
func New(logger *slog.Logger, lister PullRequestLister, merger Merger, opts ...Option) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/index.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %v", err)
	}
	s := &Server{
		logger:          logger,
		lister:          lister,
		template:        tmpl,
		now:             time.Now,
		refreshInterval: DefaultRefreshInterval,
		refreshRequests: make(chan struct{}, 1),
	}
	s.queue = newQueue(logger, merger, s.requestRefresh)
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Handler returns the HTTP handler of the server.
func (s *Server) Handler() (http.Handler, error) {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("open static files: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("GET /queue", s.handleQueue)
	mux.Handle("POST /queue", requireSameOriginJSON(http.HandlerFunc(s.handleQueueAdd)))
	mux.Handle("POST /queue/remove", requireSameOriginJSON(http.HandlerFunc(s.handleQueueRemove)))
	mux.Handle("POST /queue/clear", requireSameOriginJSON(http.HandlerFunc(s.handleQueueClear)))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	return requireLoopbackHost(mux), nil
}

// Serve serves HTTP on ln and keeps the pull requests fresh in the background, until ctx is canceled. It then shuts
// down gracefully.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	handler, err := s.Handler()
	if err != nil {
		return err
	}

	var background sync.WaitGroup
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	// Deferred in this order so that the background work is canceled before it is waited for.
	defer background.Wait()
	defer stopBackground()
	background.Go(func() { s.refreshLoop(backgroundCtx) })
	background.Go(func() { s.queue.run(backgroundCtx) })

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %v", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %v", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %v", err)
	}
	return nil
}

// pageData is the data rendered by the index template.
type pageData struct {
	Rows  []row
	Total int
	// Loaded reports whether pull requests have been fetched successfully at least once.
	Loaded bool
	// Updated is the time of the last successful fetch.
	Updated string
	// Refreshing reports whether a fetch is under way.
	Refreshing bool
	// Polling reports whether the page should reload quickly because new data is expected any moment.
	Polling bool
	// Error is the error of the last fetch, if it failed.
	Error string
	// Headers are the column headings, which sort the table when clicked.
	Headers []header
	// RefreshAction is where the refresh button posts to. It keeps the sort order and filter of the table.
	RefreshAction string
	// Filter is set when the table only shows the pull requests of one repository.
	Filter *repoFilter
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	current := s.snapshot()
	q := parsePageQuery(r.URL.Query())
	data := pageData{
		Rows:          newRows(s.now(), current.prs, q),
		Total:         len(current.prs),
		Loaded:        !current.fetchedAt.IsZero(),
		Refreshing:    current.refreshing,
		Headers:       q.headers(),
		RefreshAction: q.path("/refresh"),
	}
	if q.repo != "" {
		data.Filter = &repoFilter{Repository: q.repo, ClearHref: q.withRepo("").path("/")}
	}
	if data.Loaded {
		data.Updated = current.fetchedAt.Format(time.TimeOnly)
	}
	if current.err != nil {
		data.Error = current.err.Error()
	}
	// Until the first fetch has finished there is nothing to show, so check back soon.
	data.Polling = data.Refreshing || (!data.Loaded && data.Error == "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.template.Execute(w, data); err != nil {
		s.logger.Error("render index", "error", err)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.requestRefresh()
	// Rebuilt from the parsed state, never copied from the request, so only a sort order and a repository can end up
	// in the redirect. Both are query-encoded, so the redirect always stays on this server.
	//nolint:gosec // The target is "/" plus a query-encoded string, so it cannot point to another host.
	http.Redirect(w, r, parsePageQuery(r.URL.Query()).path("/"), http.StatusSeeOther)
}

// requireLoopbackHost rejects requests whose Host header is not a loopback name. This stops DNS-rebinding attacks
// from reading the page, or triggering actions, from a malicious website.
func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port in the Host header.
		host = hostport
	}
	switch host {
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return true
	default:
		return false
	}
}
