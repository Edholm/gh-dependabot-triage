# gh-dependabot-triage

A [GitHub CLI](https://cli.github.com) extension that opens a local web page
listing every open Dependabot pull request you have been asked to review,
directly or through a team.

For each pull request the table shows the repository, title, age, review
decision, merge status (draft, conflicts, behind, blocked, ready, auto-merge,
merge queue) and the combined CI state.

## Install

From a checkout:

```bash
make go-build
gh extension install .
```

## Usage

```bash
gh dependabot-triage
```

This starts a server on a random port on `127.0.0.1` and opens it in your
browser. Pull requests are fetched from GitHub in the background every minute
(GitHub is slow to answer, so the page never waits for it), and the page reloads
itself while it is visible. Use the Refresh button to fetch right away. Stop the
server with Ctrl-C.

| Flag           | Description                                      |
| -------------- | ------------------------------------------------ |
| `--port`       | Port to listen on (default: a random free port). |
| `--no-browser` | Print the URL instead of opening a browser.      |

Authentication is taken from `gh`, so run `gh auth login` first if you have not
already.
