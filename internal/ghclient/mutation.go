package ghclient

import (
	"context"
	"fmt"
)

// PullRequestState is whether a pull request is open, closed or merged.
type PullRequestState string

// Pull request states reported by GitHub.
const (
	PullRequestOpen   PullRequestState = "OPEN"
	PullRequestClosed PullRequestState = "CLOSED"
	PullRequestMerged PullRequestState = "MERGED"
)

// Status is the part of a pull request that decides whether, and how, it can be merged.
type Status struct {
	State PullRequestState
	// HeadOID is the commit the pull request branch points to.
	HeadOID        string
	ReviewDecision ReviewDecision
	MergeState     MergeState
	// CI is the combined state of the checks on the head commit.
	CI CheckState
	// MergeQueueRequired reports whether the base branch only accepts merges through a GitHub merge queue.
	MergeQueueRequired bool
	// InMergeQueue reports whether the pull request is already in the GitHub merge queue.
	InMergeQueue bool
}

const statusQuery = `
query($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      state
      headRefOid
      reviewDecision
      mergeStateStatus
      isMergeQueueEnabled
      mergeQueueEntry { state }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
    }
  }
}`

const approveMutation = `
mutation($id: ID!) {
  addPullRequestReview(input: {pullRequestId: $id, event: APPROVE}) { clientMutationId }
}`

// The branch is always rebased rather than merged with its base, so that the history stays linear.
const updateBranchMutation = `
mutation($id: ID!, $head: GitObjectID!) {
  updatePullRequestBranch(input: {pullRequestId: $id, expectedHeadOid: $head, updateMethod: REBASE}) {
    clientMutationId
  }
}`

const mergeMutation = `
mutation($id: ID!, $head: GitObjectID!) {
  mergePullRequest(input: {pullRequestId: $id, expectedHeadOid: $head, mergeMethod: REBASE}) { clientMutationId }
}`

const enqueueMutation = `
mutation($id: ID!, $head: GitObjectID!) {
  enqueuePullRequest(input: {pullRequestId: $id, expectedHeadOid: $head}) { clientMutationId }
}`

// Status returns the merge status of the pull request with GraphQL node ID id.
func (c *Client) Status(ctx context.Context, id string) (Status, error) {
	var response struct {
		Node *struct {
			State               PullRequestState
			HeadRefOID          string `json:"headRefOid"`
			ReviewDecision      ReviewDecision
			MergeStateStatus    MergeState
			IsMergeQueueEnabled bool
			MergeQueueEntry     *struct{ State string }
			Commits             struct {
				Nodes []struct {
					Commit struct {
						StatusCheckRollup *struct{ State CheckState }
					}
				}
			}
		}
	}
	if err := c.do(ctx, statusQuery, map[string]any{"id": id}, &response); err != nil {
		return Status{}, fmt.Errorf("get status of pull request %s: %v", id, err)
	}
	node := response.Node
	if node == nil || node.State == "" {
		return Status{}, fmt.Errorf("get status of pull request %s: not found", id)
	}
	status := Status{
		State:              node.State,
		HeadOID:            node.HeadRefOID,
		ReviewDecision:     node.ReviewDecision,
		MergeState:         node.MergeStateStatus,
		MergeQueueRequired: node.IsMergeQueueEnabled,
		InMergeQueue:       node.MergeQueueEntry != nil,
	}
	if len(node.Commits.Nodes) > 0 && node.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		status.CI = node.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	return status, nil
}

// Approve approves the pull request with GraphQL node ID id.
func (c *Client) Approve(ctx context.Context, id string) error {
	if err := c.mutate(ctx, approveMutation, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("approve pull request %s: %v", id, err)
	}
	return nil
}

// UpdateBranch rebases the pull request with GraphQL node ID id onto its base branch. It fails if the pull request
// branch no longer points to head.
func (c *Client) UpdateBranch(ctx context.Context, id, head string) error {
	if err := c.mutate(ctx, updateBranchMutation, map[string]any{"id": id, "head": head}); err != nil {
		return fmt.Errorf("rebase pull request %s: %v", id, err)
	}
	return nil
}

// Merge rebase-merges the pull request with GraphQL node ID id. It fails if the pull request branch no longer points
// to head.
func (c *Client) Merge(ctx context.Context, id, head string) error {
	if err := c.mutate(ctx, mergeMutation, map[string]any{"id": id, "head": head}); err != nil {
		return fmt.Errorf("merge pull request %s: %v", id, err)
	}
	return nil
}

// Enqueue adds the pull request with GraphQL node ID id to the GitHub merge queue of its base branch. It fails if the
// pull request branch no longer points to head.
func (c *Client) Enqueue(ctx context.Context, id, head string) error {
	if err := c.mutate(ctx, enqueueMutation, map[string]any{"id": id, "head": head}); err != nil {
		return fmt.Errorf("add pull request %s to the merge queue: %v", id, err)
	}
	return nil
}

// mutate sends a mutation once. It is never retried, see do.
func (c *Client) mutate(ctx context.Context, mutation string, variables map[string]any) error {
	var response struct{}
	return c.doer.DoWithContext(ctx, mutation, variables, &response)
}
