package site

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

const (
	bskyProfileAPI = bskyAPI + "app.bsky.actor.getProfile?actor=bsky.app"
	bskyFeedAPI    = bskyAPI + "app.bsky.feed.getAuthorFeed?actor=bsky.app&limit=30&filter=posts_and_author_threads"
	bskyThreadAPI  = bskyAPI + "app.bsky.unspecced.getPostThreadV2?anchor=at%3A%2F%2Fbsky.app%2Fapp.bsky.feed.post%2F3n27xkiekss24&below=10&branchingFactor=3&sort=top"
	bskyPostURL    = "https://bsky.app/profile/bsky.app/post/3n27xkiekss24"
)

func TestBlueskyURLs(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://bsky.app/profile/bsky.app":                                              true,
		"https://bsky.app/profile/bsky.app/":                                             true,
		"https://www.bsky.app/profile/bsky.app/post/3l6oveex3ii2l":                       true,
		"https://bsky.app/profile/did:plc:z72i7hdynmk6r22z27h6tvur/post/3l6oveex3ii2l/":  true,
		"https://bsky.app/profile/bsky.app/post/3l6oveex3ii2l/liked-by":                  false,
		"https://bsky.app/profile/bsky.app/feed/whats-hot":                               false,
		"https://bsky.app/profile/bsky.app/lists/3k2x":                                   false,
		"https://bsky.app/profile/bsky.app/follows":                                      false,
		"https://bsky.app/search?q=refetch":                                              false,
		"https://bsky.social/profile/bsky.app":                                           false,
		"https://example.com/profile/bsky.app/post/3l6oveex3ii2l":                        false,
		"https://bsky.app/profile/alicepost/3l6oveex3ii2l":                               false,
		"https://bsky.app/profile/did:plc:z72i7hdynmk6r22z27h6tvur/post/3l6oveex3ii2l?x": true,
	} {
		r := ForURL(raw)
		if got := r != nil; got != want {
			t.Errorf("ForURL(%s) = %v", raw, got)
		}
		if r != nil && r.Name != "Bluesky API" {
			t.Errorf("ForURL(%s) is the %s", raw, r.Name)
		}
	}
}

func TestBlueskyProfile(t *testing.T) {
	pages := map[string]string{bskyProfileAPI: testdata(t, "bsky-profile.json"), bskyFeedAPI: testdata(t, "bsky-feed.json")}
	var asked []string
	md, err := ForURL("https://bsky.app/profile/bsky.app").Read(t.Context(), fakeGet(pages, &asked))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"# Bluesky (@bsky.app)\n\n35716372 followers · 43 following · 923 posts · joined 2023-04-12\n\nofficial Bluesky account",
		"## Recent posts\n\n### Reposted by @bsky.app: Chuck Tingle (@chucktingle.bsky.social) · 2026-11-17 · 125 replies, 1930 reposts, 5714 likes, 68 quotes · https://bsky.app/profile/chucktingle.bsky.social/post/3n2i3pqe73k2n\n\nartist buckaroos",
		"I heard Bluesky gets all its breaking news from Flavor Flav\n\n> Quoting FLAVOR FLAV ⏰ (@flavorflav.bsky.social) · 2026-11-13 · https://bsky.app/profile/flavorflav.bsky.social/post/3n27ufkokmk2p\n>\n> Nationwide ground stop",
		"[image: Steeply climbing chart with the number 25.8 Gb/s superimposed.]",
		"### P(aul) Frazee (@pfrazee.com) · 2026-11-17 · reply to @pfrazee.com · 3 replies, 0 reposts, 35 likes · https://bsky.app/profile/pfrazee.com/post/3n2jxysjz5c24\n\n[image: It’s Metaphorical]",
		"why not\n\n[video]",
		"[bsky.social/about/join/s...](https://bsky.social/about/join/staff-site-reliability-engineer)",
		"Link: [Staff Site Reliability Engineer - Bluesky](https://bsky.social/about/join/staff-site-reliability-engineer): Staff Site Reliability Engineer",
		"ive lost control of my life\n\n[video]\n\n> Quoting cee (@cee.wtf) · 2026-11-11 · https://bsky.app/profile/cee.wtf/post/3n22svqttok25\n>\n> we can go further\n>\n> [image]",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "\n\n\n") || len(asked) != 2 {
		t.Errorf("asked %v:\n%s", asked, md)
	}

	for name, pages := range map[string]map[string]string{
		"no profile":     nil,
		"bad profile":    {bskyProfileAPI: "{"},
		"no feed":        {bskyProfileAPI: testdata(t, "bsky-profile.json")},
		"bad feed":       {bskyProfileAPI: testdata(t, "bsky-profile.json"), bskyFeedAPI: `{"feed":7}`},
		"signed-in only": {bskyProfileAPI: `{"handle":"bsky.app","labels":[{"val":"!no-unauthenticated"}]}`, bskyFeedAPI: `{"feed":[]}`},
	} {
		if _, err := ForURL("https://bsky.app/profile/bsky.app").Read(t.Context(), fakeGet(pages, &asked)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestBlueskyThread(t *testing.T) {
	var asked []string
	md, err := ForURL(bskyPostURL).Read(t.Context(), fakeGet(map[string]string{bskyThreadAPI: testdata(t, "bsky-thread.json")}, &asked))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"# Bluesky post by Bluesky (@bsky.app)\n\n## Earlier in the thread\n\n### FLAVOR FLAV ⏰ (@flavorflav.bsky.social) · 2026-11-13",
		"## The post\n\n### Bluesky (@bsky.app) · 2026-11-13 · 4 replies, 38 reposts, 1644 likes, 6 quotes · https://bsky.app/profile/bsky.app/post/3n27xkiekss24\n\nthank you for your on-the-ground reporting, Flavor Flav",
		"## Replies (4)\n\n### Ben Plume (@pittbaster.bsky.social) · 2026-11-13 · 5 replies",
		"### John Shanks (@jgshanks.bsky.social) · 2026-11-13 · reply to @pittbaster.bsky.social",
		"Link: [Ba Dum Tss Drum Roll Joke](https://static.klipy.com/",
		"### StrictlyChristo 🇺🇦🌻 (@strictlychristo.bsky.social) · 2026-11-13 · 2 replies",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}

	for name, body := range map[string]string{
		"bad":     "{",
		"empty":   `{"thread":[]}`,
		"deleted": `{"thread":[{"depth":0,"value":{"$type":"app.bsky.unspecced.defs#threadItemNotFound"}}]}`,
	} {
		if _, err := ForURL(bskyPostURL).Read(t.Context(), fakeGet(map[string]string{bskyThreadAPI: body}, &asked)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ForURL(bskyPostURL).Read(t.Context(), fakeGet(nil, &asked)); err == nil {
		t.Error("a failed API call")
	}
}

// bskyTestItem renders one thread item for a synthetic thread.
func bskyTestItem(depth int, kind, handle, extra string) string {
	return fmt.Sprintf(`{"depth":%d,"value":{"$type":"app.bsky.unspecced.defs#%s","post":{"uri":"at://did:plc:x/app.bsky.feed.post/r%d",
		"author":{"did":"did:plc:x","handle":%q%s},"record":{"text":"by %s","createdAt":"2026-11-01T00:00:00Z"}}}}`, depth, kind, depth, handle, extra, handle)
}

func TestBlueskyThreadEdges(t *testing.T) {
	items := []string{
		bskyTestItem(0, "threadItemPost", "op.example", ""),
		bskyTestItem(1, "threadItemBlocked", "", ""),
		bskyTestItem(2, "threadItemPost", "child.example", ""),
		bskyTestItem(1, "threadItemNoUnauthenticated", "", ""),
		bskyTestItem(1, "threadItemPost", "shy.example", `,"labels":[{"val":"!no-unauthenticated"}]`),
		bskyTestItem(1, "threadItemSomethingNew", "", ""),
		bskyTestItem(1, "threadItemPost", "handle.invalid", ""),
	}
	for range bskyMaxReplies {
		items = append(items, bskyTestItem(1, "threadItemPost", "many.example", ""))
	}
	var asked []string
	md, err := ForURL(bskyPostURL).Read(t.Context(), fakeGet(map[string]string{bskyThreadAPI: `{"thread":[` + strings.Join(items, ",") + `]}`}, &asked))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"### (post from a blocked account)\n\n### @child.example · 2026-11-01 · 0 replies",
		"### (the author shows this post only to signed-in users)",
		"### @shy.example · 2026-11-01 · 0 replies, 0 reposts, 0 likes · https://bsky.app/profile/shy.example/post/r1\n\n(The author shows this post only to signed-in users.)",
		"### (post unavailable)",
		"https://bsky.app/profile/did:plc:x/post/r1",
		"\n\nOnly the first 80 replies are shown.",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%.3000s", w, md)
		}
	}
	if strings.Contains(md, "by shy.example") || strings.Contains(md, "@child.example · 2026-11-01 · reply to") {
		t.Errorf("hidden text or a reply to a missing post:\n%.3000s", md)
	}
	odd := `{"thread":[` + bskyTestItem(1, "threadItemPost", "early.example", "") + "," + bskyTestItem(0, "threadItemPost", "op.example", "") + `]}`
	if md, err := ForURL(bskyPostURL).Read(t.Context(), fakeGet(map[string]string{bskyThreadAPI: odd}, &asked)); err != nil || !strings.Contains(md, "by early.example") {
		t.Errorf("a reply ahead of the post: %v\n%s", err, md)
	}
}

func TestBlueskyEmbeds(t *testing.T) {
	var embeds []bskyEmbed
	if err := json.Unmarshal([]byte(`[
		{"$type":"app.bsky.embed.record#view","record":{"$type":"app.bsky.embed.record#viewNotFound"}},
		{"$type":"app.bsky.embed.record#view","record":{"$type":"app.bsky.embed.record#viewRecord","author":{"handle":"shy.example","labels":[{"val":"!no-unauthenticated"}]}}},
		{"$type":"app.bsky.embed.record#view","record":{"$type":"app.bsky.feed.defs#generatorView"}},
		{"$type":"app.bsky.embed.external#view","external":{"uri":"https://example.com/a"}}]`), &embeds); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range embeds {
		writeBskyEmbed(&b, e, true)
	}
	want := "\n> Quoting a post that is deleted, blocked or detached\n\n> Quoting a post its author shows only to signed-in users\n\nLink: [https://example.com/a](https://example.com/a)\n"
	if b.String() != want {
		t.Errorf("embeds:\n%q\nwant\n%q", b.String(), want)
	}

	var r bskyRecord
	if err := json.Unmarshal([]byte(`{"text":"see example.com/a… and ftp, then x","facets":[
		{"index":{"byteStart":4,"byteEnd":17},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.com/a/long"}]},
		{"index":{"byteStart":10,"byteEnd":12},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://overlap.example"}]},
		{"index":{"byteStart":25,"byteEnd":28},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"ftp://example.com"}]},
		{"index":{"byteStart":33,"byteEnd":99},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://out.of.range"}]}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := bskyText(r); got != "see [example.com/a](https://example.com/a/long)… and ftp, then x" {
		t.Errorf("bskyText = %q", got)
	}
	if bskyLink(bskyAuthor{Handle: "a.example"}, "at://did:plc:x/app.bsky.feed.like/1") != "" {
		t.Error("a like is no post")
	}
}
