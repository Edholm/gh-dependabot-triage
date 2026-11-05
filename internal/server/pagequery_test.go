package server

import (
	"net/url"
	"testing"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"gotest.tools/v3/assert"
)

func TestPageQuery_PathRoundTrips(t *testing.T) {
	t.Parallel()
	for _, c := range columns() {
		for _, descending := range []bool{false, true} {
			for _, repo := range []string{"", "acme/api", "acme/a&b=c d"} {
				want := pageQuery{order: order{column: c, descending: descending}, repo: repo}

				target, err := url.Parse(want.path("/"))
				assert.NilError(t, err)

				assert.Equal(t, parsePageQuery(target.Query()), want)
			}
		}
	}
	assert.Equal(t, pageQuery{order: defaultOrder()}.path("/refresh"), "/refresh", "the default state needs no query")
	assert.Equal(t, pageQuery{order: defaultOrder(), repo: "acme/api"}.path("/"), "/?repo=acme%2Fapi")
}

func TestPageQuery_Headers(t *testing.T) {
	t.Parallel()

	headers := pageQuery{order: order{column: columnCI, descending: true}}.headers()

	assert.DeepEqual(t, headers, []header{
		{Label: "Repository", Href: "/"},
		{Label: "Pull request", Href: "/?dir=asc&sort=title"},
		{Label: "Age", Href: "/?dir=desc&sort=age"},
		{Label: "Review", Href: "/?dir=asc&sort=review"},
		{Label: "Status", Href: "/?dir=asc&sort=status"},
		{Label: "CI", Href: "/?dir=asc&sort=ci", Sort: "descending", Arrow: "▼"},
	})
	assert.Equal(t, pageQuery{order: defaultOrder()}.headers()[0].Sort, "ascending")
}

func TestPageQuery_HeadersKeepRepositoryFilter(t *testing.T) {
	t.Parallel()

	headers := pageQuery{order: defaultOrder(), repo: "acme/api"}.headers()

	assert.Equal(t, headers[0].Href, "/?dir=desc&repo=acme%2Fapi&sort=repo")
	assert.Equal(t, headers[1].Href, "/?dir=asc&repo=acme%2Fapi&sort=title")
}

func TestPageQuery_Filter(t *testing.T) {
	t.Parallel()
	prs := []ghclient.PullRequest{
		{Repository: "acme/api", Number: 1},
		{Repository: "acme/web", Number: 2},
		{Repository: "acme/api", Number: 3},
	}

	got := pageQuery{repo: "acme/api"}.filter(prs)

	assert.DeepEqual(t, got, []ghclient.PullRequest{prs[0], prs[2]})
	assert.Equal(t, len(prs), 3, "the input must not be modified")
	assert.Equal(t, prs[2].Number, 3, "the input must not be modified")
	assert.Equal(t, len(pageQuery{}.filter(prs)), 3, "no repository shows everything")
	assert.Equal(t, len(pageQuery{repo: "acme/nope"}.filter(prs)), 0)
}
