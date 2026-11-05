// Package ghclient lists the Dependabot pull requests a user has been asked to review.
package ghclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	// searchQuery selects open Dependabot pull requests where the viewer, or a team they belong to,
	// is a requested reviewer.
	searchQuery = "is:pr is:open archived:false author:app/dependabot review-requested:@me"
	// dependabotLogin is the GraphQL login of the Dependabot bot account.
	dependabotLogin = "dependabot"
	// pageSize is deliberately small: GitHub computes merge state for every pull request in a page, so large pages
	// are slow and often time out.
	pageSize = 25
	// maxAttempts is how many times a request is tried when GitHub answers with a transient server error.
	maxAttempts = 3
	// defaultRetryDelay is the base delay between attempts.
	defaultRetryDelay = time.Second
)

const listQuery = `
query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        bodyText
        url
        isDraft
        createdAt
        updatedAt
        author { login }
        repository { nameWithOwner }
        reviewDecision
        mergeStateStatus
        autoMergeRequest { enabledAt }
        mergeQueueEntry { state position }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`

// Doer executes GraphQL requests. It is satisfied by the go-gh GraphQL client.
type Doer interface {
	DoWithContext(ctx context.Context, query string, variables map[string]any, response any) error
}

// Client lists pull requests from GitHub.
type Client struct {
	doer       Doer
	retryDelay time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithRetryDelay sets the base delay between attempts at a request that failed with a transient server error. The
// delay grows linearly with each attempt.
func WithRetryDelay(delay time.Duration) Option {
	return func(c *Client) { c.retryDelay = delay }
}

// New returns a Client that sends its requests through doer.
func New(doer Doer, opts ...Option) *Client {
	c := &Client{doer: doer, retryDelay: defaultRetryDelay}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ListReviewRequested returns all open Dependabot pull requests that the authenticated user has been requested to
// review, directly or through a team.
func (c *Client) ListReviewRequested(ctx context.Context) ([]PullRequest, error) {
	var result []PullRequest
	var after *string
	for {
		var response searchResponse
		variables := map[string]any{"q": searchQuery, "first": pageSize, "after": after}
		if err := c.do(ctx, variables, &response); err != nil {
			return nil, fmt.Errorf("search pull requests: %v", err)
		}
		for _, node := range response.Search.Nodes {
			// The search qualifier already restricts the author; this guards against anything slipping through.
			if node.Number == 0 || node.Author == nil || node.Author.Login != dependabotLogin {
				continue
			}
			result = append(result, node.pullRequest())
		}
		if !response.Search.PageInfo.HasNextPage {
			return result, nil
		}
		cursor := response.Search.PageInfo.EndCursor
		after = &cursor
	}
}

// do sends the list query. GitHub's search regularly answers 502 while it computes merge states, so transient server
// errors are retried a few times.
func (c *Client) do(ctx context.Context, variables map[string]any, response *searchResponse) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = c.doer.DoWithContext(ctx, listQuery, variables, response)
		if err == nil || !isTransient(err) || attempt == maxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * c.retryDelay):
		}
	}
	return err
}

// isTransient reports whether err is a server-side error that is likely to go away on its own.
func isTransient(err error) bool {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

type searchResponse struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool
			EndCursor   string
		}
		Nodes []pullRequestNode
	}
}

type pullRequestNode struct {
	Number           int
	Title            string
	BodyText         string
	URL              string
	IsDraft          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Author           *struct{ Login string }
	Repository       struct{ NameWithOwner string }
	ReviewDecision   ReviewDecision
	MergeStateStatus MergeState
	AutoMergeRequest *struct{ EnabledAt time.Time }
	MergeQueueEntry  *struct {
		State    string
		Position int
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State CheckState }
			}
		}
	}
}

func (n pullRequestNode) pullRequest() PullRequest {
	pr := PullRequest{
		Repository:     n.Repository.NameWithOwner,
		Number:         n.Number,
		Title:          n.Title,
		Description:    n.BodyText,
		URL:            n.URL,
		Author:         n.Author.Login,
		Draft:          n.IsDraft,
		CreatedAt:      n.CreatedAt,
		UpdatedAt:      n.UpdatedAt,
		ReviewDecision: n.ReviewDecision,
		MergeState:     n.MergeStateStatus,
		AutoMerge:      n.AutoMergeRequest != nil,
	}
	if n.MergeQueueEntry != nil {
		pr.MergeQueue = &MergeQueueEntry{State: n.MergeQueueEntry.State, Position: n.MergeQueueEntry.Position}
	}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		pr.CI = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	return pr
}
