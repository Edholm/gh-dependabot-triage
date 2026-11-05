package server

import (
	"context"
	"fmt"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

// state is what the server knows about GitHub. It is guarded by Server.mu.
type state struct {
	// prs is the result of the last successful fetch.
	prs []ghclient.PullRequest
	// fetchedAt is the time of the last successful fetch. It is zero until there has been one.
	fetchedAt time.Time
	// err is the error of the last fetch, or nil if it succeeded.
	err error
	// refreshing is set while a fetch is under way or has been requested.
	refreshing bool
}

// snapshot returns a copy of the current state.
func (s *Server) snapshot() state {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// requestRefresh asks the refresh loop to fetch right away. It never blocks.
func (s *Server) requestRefresh() {
	s.mu.Lock()
	s.state.refreshing = true
	s.mu.Unlock()
	select {
	case s.refreshRequests <- struct{}{}:
	default:
		// A refresh is already pending.
	}
}

// Refresh fetches the pull requests from GitHub and records the outcome. If the fetch fails, the pull requests of
// the last successful fetch are kept, so that a flaky connection does not blank the page.
func (s *Server) Refresh(ctx context.Context) error {
	s.mu.Lock()
	s.state.refreshing = true
	s.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	prs, err := s.lister.ListReviewRequested(fetchCtx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.refreshing = false
	if err != nil {
		s.state.err = err
		return fmt.Errorf("refresh pull requests: %v", err)
	}
	s.state.prs = prs
	s.state.fetchedAt = s.now()
	s.state.err = nil
	return nil
}

// refreshLoop fetches the pull requests right away, then again every refresh interval or whenever a refresh is
// requested, until ctx is canceled.
func (s *Server) refreshLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.refreshRequests:
		}
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("refresh failed", "error", err)
		}
		timer.Reset(s.refreshInterval)
	}
}
