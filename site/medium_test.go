package site

import (
	"strings"
	"testing"
)

func TestMediumURLs(t *testing.T) {
	for raw, want := range map[string]string{
		"https://medium.com/@karpathy/software-2-0-a64152b37c35":                 "https://medium.com/feed/@karpathy",
		"https://www.medium.com/@karpathy/a64152b37c35?source=rss":               "https://medium.com/feed/@karpathy",
		"https://medium.com/data-science/deep-dive-aac387e10cb6/":                "https://medium.com/feed/data-science",
		"https://karpathy.medium.com/software-2-0-a64152b37c35":                  "https://karpathy.medium.com/feed",
		"https://medium.com/p/a64152b37c35":                                      "",
		"https://medium.com/tag/go-a64152b37c35":                                 "",
		"https://medium.com/@karpathy":                                           "",
		"https://medium.com/@karpathy/software-2-0":                              "",
		"https://medium.com/@karpathy/list/reading-list-a64152b37c35":            "",
		"https://karpathy.medium.com/":                                           "",
		"https://karpathy.medium.com/@karpathy/software-2-0-a64152b37c35":        "",
		"https://a.b.medium.com/software-2-0-a64152b37c35":                       "",
		"https://notmedium.com/@karpathy/software-2-0-a64152b37c35":              "",
		"https://medium.com.example.com/@karpathy/software-2-0-a64152b37c35":     "",
		"https://medium.com/@karpathy/software-2-0-a64152b37c35/responses/x1234": "",
	} {
		r := ForURL(raw)
		if (r != nil) != (want != "") {
			t.Errorf("ForURL(%s) = %v", raw, r != nil)
			continue
		}
		if r == nil {
			continue
		}
		var asked []string
		_, _ = r.Read(t.Context(), fakeGet(nil, &asked))
		if r.Name != "Medium RSS feed" || len(asked) != 1 || asked[0] != want {
			t.Errorf("ForURL(%s): %s asked %v, want %s", raw, r.Name, asked, want)
		}
	}
}

func TestMedium(t *testing.T) {
	feed := map[string]string{"https://medium.com/feed/data-science": testdata(t, "medium-feed.xml")}
	var asked []string
	md, err := ForURL("https://medium.com/data-science/deep-dive-into-websockets-aac387e10cb6").Read(t.Context(), fakeGet(feed, &asked))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"# Deep Dive into WebSockets and Their Role in Client-Server Communication\n\nby clara · 2025-02-03 · tags: software-development, system-design-concepts, websocket",
		"#### How WebSockets work, its tradeoffs",
		"Image by [Kelly](https://unsplash.com/@kellysikkema)",
		"### Client-server communication",
		"```\nGET /chat HTTP/1.1\nHost: server.example.com",
		"- Upgrade — signals the request to switch the protocol",
		"was originally published in [TDS Archive](https://medium.com/data-science)",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "_/stat") {
		t.Errorf("the tracking pixel:\n%s", md)
	}

	for name, c := range map[string]struct{ url, feed, want string }{
		"preview only": {"https://medium.com/data-science/quantifying-surprise-1eb9585b6f4e", testdata(t, "medium-feed.xml"), "only a preview"},
		"too old":      {"https://medium.com/data-science/old-post-0123456789ab", testdata(t, "medium-feed.xml"), "only the newest posts"},
		"bad feed":     {"https://medium.com/data-science/old-post-0123456789ab", "<rss><channel><item>", "decoding the feed"},
		"no feed":      {"https://medium.com/@nobody/old-post-0123456789ab", "", "HTTP 404"},
	} {
		pages := map[string]string{"https://medium.com/feed/data-science": c.feed}
		if _, err := ForURL(c.url).Read(t.Context(), fakeGet(pages, &asked)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
