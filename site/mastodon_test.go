package site

import (
	"os"
	"strings"
	"testing"
)

func testdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const msAPI = "https://mastodon.social/api/v1/"

func TestMastodonMatch(t *testing.T) {
	page := []byte(testdata(t, "mastodon_profile.html"))
	for raw, want := range map[string]bool{
		"https://mastodon.social/@Mastodon":                     true,
		"https://mastodon.social/@Mastodon/":                    true,
		"https://mastodon.social/@fosstodon@fosstodon.org":      true,
		"https://mastodon.social/@Mastodon/117466985005297414":  true,
		"https://mastodon.social/@Mastodon/with_replies":        false,
		"https://mastodon.social/explore":                       false,
		"https://mastodon.social/tags/Mastodon":                 false,
		"https://mastodon.social/@Mastodon/117466985005297414/": true,
	} {
		if got := ForPage(raw, page) != nil; got != want {
			t.Errorf("ForPage(%s) = %v", raw, got)
		}
	}
	if ForPage("https://blog.example.com/@ada", []byte(`<html><div id="mastodon">a fan page</div></html>`)) != nil {
		t.Error("a page without the app's initial state is no Mastodon instance")
	}
}

func TestMastodonProfile(t *testing.T) {
	var asked []string
	r := ForPage("https://mastodon.social/@Mastodon", []byte(testdata(t, "mastodon_profile.html")))
	md, err := r.Read(t.Context(), fakeGet(map[string]string{
		msAPI + "accounts/lookup?acct=Mastodon":                         testdata(t, "mastodon_account.json"),
		msAPI + "accounts/13179/statuses?limit=20&exclude_replies=true": testdata(t, "mastodon_statuses.json"),
	}, &asked))
	want := []string{
		"# Mastodon (@Mastodon@mastodon.social)\n\n568 posts · 51 following · 878824 followers · joined 2016-11-23\n",
		"Our mission is to connect the world",
		"- Homepage: https://joinmastodon.org\n- Source Code: https://github.com/mastodon\n",
		"## Recent posts, replies left out (3)",
		"### Mastodon (@Mastodon@mastodon.social) · 2026-11-16 11:05 UTC\n",
		"4 replies · 65 boosts · 124 favourites · https://mastodon.social/@Mastodon/117625790631441803",
		"### Mastodon (@Mastodon@mastodon.social) boosted",
		"We just released Mastodon 4.7.0!",
		"- image https://files.mastodon.social/media_attachments/",
		"The Mastodon profile page for Sage, the demo cat.",
	}
	for _, w := range want {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q", w)
		}
	}
	if err != nil || r.Name != "Mastodon API" {
		t.Errorf("%v, %s", err, r.Name)
	}
}

func TestMastodonThread(t *testing.T) {
	var asked []string
	const id = "117466985005297414"
	r := ForPage("https://mastodon.social/@Mastodon/"+id, []byte(testdata(t, "mastodon_profile.html")))
	md, err := r.Read(t.Context(), fakeGet(map[string]string{
		msAPI + "statuses/" + id:              testdata(t, "mastodon_status.json"),
		msAPI + "statuses/" + id + "/context": testdata(t, "mastodon_context.json"),
	}, &asked))
	want := []string{
		"# Post by Mastodon (@Mastodon@mastodon.social)\n\n## Earlier in the thread\n",
		"getting a new look",
		"## The post\n\n### Mastodon (@Mastodon@mastodon.social) · 2026-10-19 09:59 UTC · replying to @Mastodon@mastodon.social\n",
		"new community forum",
		"## Replies (3)",
		"### Ada (@ada@example.social)",
		"### Bob the Builder (@bob@example.org) · ",
		"replying to @ada@example.social\n\nContent warning: login talk\n\nIt does, see https://forum.example.org/login\n",
		"### Cy (@cy@mastodon.social)",
	}
	for _, w := range want {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q", w)
		}
	}
	if err != nil {
		t.Error(err)
	}
}

func TestMastodonErrors(t *testing.T) {
	page := []byte(testdata(t, "mastodon_profile.html"))
	profile := ForPage("https://mastodon.social/@Mastodon", page)
	thread := ForPage("https://mastodon.social/@Mastodon/7", page)
	for name, c := range map[string]struct {
		r     *Route
		pages map[string]string
	}{
		"limited mode":    {profile, nil},
		"bad account":     {profile, map[string]string{msAPI + "accounts/lookup?acct=Mastodon": "{"}},
		"no account":      {profile, map[string]string{msAPI + "accounts/lookup?acct=Mastodon": "{}"}},
		"no statuses":     {profile, map[string]string{msAPI + "accounts/lookup?acct=Mastodon": `{"id":"1"}`}},
		"refused status":  {thread, nil},
		"refused context": {thread, map[string]string{msAPI + "statuses/7": `{"id":"7"}`}},
		"broken context":  {thread, map[string]string{msAPI + "statuses/7": `{"id":"7"}`, msAPI + "statuses/7/context": "["}},
	} {
		var asked []string
		if _, err := c.r.Read(t.Context(), fakeGet(c.pages, &asked)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	var asked []string
	md, err := thread.Read(t.Context(), fakeGet(map[string]string{
		msAPI + "statuses/7": `{"id":"7","content":"","account":{"acct":"ada","username":"ada"},
			"poll":{"options":[{"title":"Yes","votes_count":3},{"title":"No","votes_count":null}]},"media_attachments":[{"type":"image","url":"https://x/1.png"}]}`,
		msAPI + "statuses/7/context": `{"ancestors":[],"descendants":[]}`,
	}, &asked))
	if err != nil || !strings.Contains(md, "# Post by ada (@ada@mastodon.social)\n\n### ada") || !strings.Contains(md, "Poll: Yes (3) · No (0)") ||
		!strings.Contains(md, "- image https://x/1.png: (no description)") || strings.Contains(md, "## Replies") {
		t.Errorf("%v\n%s", err, md)
	}
}
