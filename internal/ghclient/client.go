// Package ghclient lists the Dependabot pull requests a user has been asked to review, and approves and merges them.
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
	// searchBase selects open Dependabot pull requests. The searches below narrow it down.
	searchBase = "is:pr is:open archived:false author:app/dependabot"
	// reviewRequestedQuery selects the pull requests where the viewer, or a team they belong to, is a requested
	// reviewer.
	reviewRequestedQuery = searchBase + " review-requested:@me"
	// reviewedQuery selects the pull requests the viewer has reviewed. Reviewing a pull request fulfills the review
	// request, so without this search a pull request would vanish from the list as soon as it is approved, before it
	// can be merged.
	reviewedQuery = searchBase + " reviewed-by:@me"
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
        id
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
        viewerLatestReview { state }
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

// ListPullRequests returns all open Dependabot pull requests that the authenticated user has been requested to
// review, directly or through a team, or has reviewed already. GitHub search has no "or" for qualifiers, so this
// runs one search for each and merges their results.
func (c *Client) ListPullRequests(ctx context.Context) ([]PullRequest, error) {
	var result []PullRequest
	seen := make(map[string]bool)
	for _, query := range []string{reviewRequestedQuery, reviewedQuery} {
		prs, err := c.search(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			key := fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, pr)
		}
	}
	return result, nil
}

// search returns all Dependabot pull requests that query finds.
func (c *Client) search(ctx context.Context, query string) ([]PullRequest, error) {
	var result []PullRequest
	var after *string
	for {
		var response searchResponse
		variables := map[string]any{"q": query, "first": pageSize, "after": after}
		if err := c.do(ctx, listQuery, variables, &response); err != nil {
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

// do sends a read-only query. GitHub regularly answers 502 while it computes merge states, so transient server errors
// are retried a few times. Mutations must not go through do: a mutation that timed out may still have been applied.
func (c *Client) do(ctx context.Context, query string, variables map[string]any, response any) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = c.doer.DoWithContext(ctx, query, variables, response)
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

// The response types below spell out the JSON name of every field. go-gh decodes responses with encoding/json, which
// matches names case-insensitively, while encoding/json/v2 matches them exactly; explicit names decode the same with
// both.

type searchResponse struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []pullRequestNode `json:"nodes"`
	} `json:"search"`
}

type pullRequestNode struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	BodyText  string    `json:"bodyText"`
	URL       string    `json:"url"`
	IsDraft   bool      `json:"isDraft"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Author    *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	ReviewDecision     ReviewDecision `json:"reviewDecision"`
	ViewerLatestReview *struct {
		State string `json:"state"`
	} `json:"viewerLatestReview"`
	MergeStateStatus MergeState `json:"mergeStateStatus"`
	AutoMergeRequest *struct {
		EnabledAt time.Time `json:"enabledAt"`
	} `json:"autoMergeRequest"`
	MergeQueueEntry *struct {
		State    string `json:"state"`
		Position int    `json:"position"`
	} `json:"mergeQueueEntry"`
	Commits lastCommit `json:"commits"`
}

// lastCommit is the commits(last: 1) connection of a pull request, with the CI state of that commit.
type lastCommit struct {
	Nodes []struct {
		Commit struct {
			StatusCheckRollup *struct {
				State CheckState `json:"state"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	} `json:"nodes"`
}

// ci returns the combined CI state of the commit, or an empty state if it has no checks.
func (c lastCommit) ci() CheckState {
	if len(c.Nodes) == 0 || c.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return c.Nodes[0].Commit.StatusCheckRollup.State
}

func (n pullRequestNode) pullRequest() PullRequest {
	pr := PullRequest{
		ID:             n.ID,
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
		ViewerApproved: n.ViewerLatestReview != nil && n.ViewerLatestReview.State == "APPROVED",
		MergeState:     n.MergeStateStatus,
		AutoMerge:      n.AutoMergeRequest != nil,
		CI:             n.Commits.ci(),
	}
	if n.MergeQueueEntry != nil {
		pr.MergeQueue = &MergeQueueEntry{State: n.MergeQueueEntry.State, Position: n.MergeQueueEntry.Position}
	}
	return pr
}
