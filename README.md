# gh-dependabot-triage

A [GitHub CLI](https://cli.github.com) extension that opens a local web page
listing every open Dependabot pull request you have been asked to review,
directly or through a team, or have reviewed already. Reviewed pull requests
stay on the list so that you can still merge them after approving them.

For each pull request the table shows the repository, title, age, review
decision, merge status (draft, conflicts, behind, blocked, ready, auto-merge,
merge queue) and the combined CI state. Hovering a title shows the start of the
pull request description.

## Install

From a checkout, which also upgrades an extension installed this way:

```bash
make install
```

## Usage

```bash
gh dependabot-triage
```

This starts a server on port 7826 of `127.0.0.1` and opens it in your browser.
The port is fixed because the browser keeps the page's settings (theme, fix
prompt) per port; when it is taken, a random port is used instead, without those
settings. Pull requests are fetched from GitHub in the background every minute
(GitHub is slow to answer, so the page never waits for it), and the page
refreshes itself in place while it is visible. Use the Refresh button to fetch
right away. Stop the server with Ctrl-C.

| Flag           | Description                                                  |
| -------------- | ------------------------------------------------------------ |
| `--port`       | Port to listen on (default: 7826, 0 for a random free port). |
| `--org`        | Only show pull requests in this organization (or user).      |
| `--no-browser` | Print the URL instead of opening a browser.                  |

Authentication is taken from `gh`, so run `gh auth login` first if you have not
already.

## Theme

The footer has a theme switcher: System (the default, follows your operating
system), Light or Dark. The choice is saved in your browser's local storage.

## Filtering by repository

Click a repository to show only its pull requests, and click "Show all" above
the table to go back. The filter is kept in the URL together with the sort
order, so it survives the automatic reload and the Refresh button.

## Sorting

Click a column heading to sort by it, and click it again to reverse the order.
The sort order is kept in the URL, so it survives the automatic reload and the
Refresh button. Review, status and CI sort from the most ready to merge (for
example passing CI first), and Age starts with the oldest pull request. Rows
that are equal in the sorted column stay ordered by repository and number.

## Approving and merging

Tick pull requests to select them, or use the Select buttons above the table:
"Passing CI" selects every pull request shown with passing CI that is not a
draft, has no conflicts and is not already in a merge queue, and "Failing CI"
selects every pull request shown with failing CI. Shift-click a box to select a
range. A selection is kept while you sort and filter, in this tab only.

While pull requests are selected, an island at the bottom of the page offers:

- **Copy fix prompt** copies a prompt asking an AI agent to fix CI of the
  selected pull requests, with their links. By default it asks for each fix to
  be folded into the Dependabot commit. Edit the prompt with the ✎ button next
  to it: `{links}` in it is replaced by the links, one per line, and without it
  the links are added at the end. The prompt is saved in your browser's local
  storage.
- **Approve** approves them.
- **Approve & merge** approves all of them right away, then merges them one at a
  time per repository, in the order the table shows them. Pull requests of
  different repositories are merged in parallel. Before each merge it waits for
  CI. When a pull request is behind its base branch, it is rebased onto it
  (never merged with it), and merged once CI passes again. Merges always
  rebase-merge. When a repository requires a GitHub merge queue, the pull
  request is added to that queue instead, and the work is done.

A pull request fails, and the queue moves on to the next one, when CI fails, it
has conflicts, it still needs a review after the approval, a merge fails twice,
or it does not become mergeable within 30 minutes of its last rebase. The island
lists the queue with the state of each pull request. Pull requests that are
still waiting, including those waiting for CI, can be removed from it; one that
is being approved, rebased or merged cannot.

The queue runs in the server, so it keeps going when you close the page, but it
stops when you stop the server.
