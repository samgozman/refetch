package site

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGet answers from a map of URL to body; other URLs fail.
func fakeGet(pages map[string]string, asked *[]string) Getter {
	return func(_ context.Context, u string) ([]byte, error) {
		*asked = append(*asked, u)
		if b, ok := pages[u]; ok {
			return []byte(b), nil
		}
		return nil, errors.New("HTTP 404")
	}
}

// failGet fails every URL as the getter does for an HTTP error status: the
// error, and the answer's body.
func failGet(body string) Getter {
	return func(context.Context, string) ([]byte, error) {
		return []byte(body), errors.New("HTTP 400 (chrome_like client)")
	}
}

func TestRateLimited(t *testing.T) {
	se, gh := ForURL("https://superuser.com/q/42"), ForPage("https://github.com/a/b/issues/2", []byte(`<div data-testid="issue-timeline-load-more">`))
	for _, tc := range []struct {
		name    string
		r       *Route
		body    string
		limited bool
		want    string
	}{
		{"SE throttle", se, `{"error_id":502,"error_message":"too many requests from this IP, more requests available in 82626 seconds","error_name":"throttle_violation"}`, true, "more requests available in 82626 seconds"},
		{"SE bad site", se, `{"error_id":400,"error_message":"site is required","error_name":"bad_parameter"}`, false, "site is required"},
		{"SE no JSON", se, `<html>Bad request</html>`, false, "HTTP 400"},
		{"GitHub primary", gh, `{"message":"API rate limit exceeded for 203.0.113.9. (But here's the good news: Authenticated requests get a higher rate limit.)"}`, true, "HTTP 400"},
		{"GitHub secondary", gh, `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`, true, "HTTP 400"},
		{"GitHub other", gh, `{"message":"Not Found"}`, false, "HTTP 400"},
	} {
		_, err := tc.r.Read(t.Context(), failGet(tc.body))
		if err == nil || errors.Is(err, ErrRateLimited) != tc.limited || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "203.0.113.9") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

const seAPI = "https://api.stackexchange.com/2.3/questions/42?site=superuser.com&filter=%21%29PiAlwRxgCI.Y8TjE4%28kd0i%28uOnH4D6lbuV0GICG8wNXvaML4iKA"

func TestStackExchange(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://superuser.com/questions/42/how-big-is-a-folder": true,
		"https://superuser.com/q/42":                             true,
		"https://unix.stackexchange.com/questions/7/x":           true,
		"https://ru.stackoverflow.com/questions/7":               true,
		"https://api.stackexchange.com/questions/7":              false,
		"https://superuser.com/users/42/someone":                 false,
		"https://example.com/questions/42/x":                     false,
		"ftp://superuser.com/questions/42":                       false,
	} {
		if got := ForURL(raw) != nil; got != want {
			t.Errorf("ForURL(%s) = %v", raw, got)
		}
	}
	body := `{"items":[{"title":"How big is a folder &quot;here&quot;?","body_markdown":"Use ` + "`du -sh`" + ` &amp; wait.","score":12,
		"owner":{"display_name":"ada"},"creation_date":1700000000,"tags":["bash","du"],
		"comments":[{"body_markdown":"Which OS?","owner":{"display_name":"bob"},"score":2}],
		"answers":[{"body_markdown":"Low score.","score":1,"owner":{"display_name":"cy"},"creation_date":1700000100},
			{"body_markdown":"Try **ncdu**.","score":30,"owner":{"display_name":"di"}},
			{"body_markdown":"Accepted one.","score":5,"is_accepted":true,"owner":{"display_name":"ed"}}]}],"quota_remaining":299}`
	var asked []string
	md, err := ForURL("https://superuser.com/questions/42/how-big-is-a-folder").Read(t.Context(), fakeGet(map[string]string{seAPI: body}, &asked))
	want := []string{`# How big is a folder "here"?`, "score 12 · asked by ada on 2023-11-14 · tags: bash, du", "Use `du -sh` & wait.",
		"- Which OS? (bob, score 2)", "## 3 answers", "### Accepted answer · score 5 · ed"}
	for _, w := range want {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}
	if err != nil || strings.Index(md, "Accepted one") > strings.Index(md, "ncdu") || strings.Index(md, "ncdu") > strings.Index(md, "Low score") {
		t.Errorf("answers in order accepted, then by score: %v\n%s", err, md)
	}
	for name, body := range map[string]string{
		"error": `{"error_id":502,"error_message":"too many requests from this IP"}`, "none": `{"items":[]}`, "bad": `{`,
	} {
		if _, err := ForURL("https://superuser.com/q/42").Read(t.Context(), fakeGet(map[string]string{seAPI: body}, &asked)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDiscourse(t *testing.T) {
	page := []byte(`<html><head><title>Raw posts &amp; more - Meta</title><meta name="generator" content="Discourse 2026.11.0"></head><body></body></html>`)
	if ForPage("https://forum.example.com/t/raw-posts/324716", []byte("<html><title>Not a forum</title></html>")) != nil {
		t.Error("a page without the Discourse generator is no Discourse topic")
	}
	if ForPage("https://forum.example.com/latest", page) != nil {
		t.Error("a topic list is no topic")
	}
	r := ForPage("https://example.com/forum/t/raw-posts/324716/3", page)
	if r == nil {
		t.Fatal("a Discourse topic under a subfolder")
	}
	var asked []string
	md, err := r.Read(t.Context(), fakeGet(map[string]string{
		"https://example.com/forum/raw/324716?page=1": "ada | 2024-09-03 | #1\n\nFirst post.\n\n-------------------------\n",
		"https://example.com/forum/raw/324716?page=2": "bob | 2024-09-04 | #101\n\nPage two.\n",
		"https://example.com/forum/raw/324716?page=3": "\n",
	}, &asked))
	if err != nil || !strings.HasPrefix(md, "# Raw posts & more - Meta\n\nada | 2024-09-03 | #1") || !strings.Contains(md, "Page two.") || len(asked) != 3 {
		t.Errorf("%v, asked %v:\n%s", err, asked, md)
	}
	if _, err := r.Read(t.Context(), fakeGet(nil, &asked)); err == nil {
		t.Error("a topic whose /raw fails")
	}
}

func TestGitHub(t *testing.T) {
	long := []byte(`<html><title>Issue</title><div data-testid="issue-timeline-load-more"></div></html>`)
	if ForPage("https://github.com/golang/go/issues/1", []byte("<html>all comments here</html>")) != nil {
		t.Error("an issue whose page holds every comment is read from the page")
	}
	if ForPage("https://github.com/golang/go/discussions/1", long) != nil || ForPage("https://gitlab.com/a/b/issues/1", long) != nil {
		t.Error("only GitHub issues and pull requests")
	}
	api := "https://api.github.com/repos/golang/go/issues/48918"
	comments := "[" + strings.TrimSuffix(strings.Repeat(`{"body":"+1","user":{"login":"x"},"created_at":"2021-10-13T01:02:03Z","author_association":"NONE"},`, 100), ",") + "]"
	var asked []string
	md, err := ForPage("https://github.com/golang/go/pull/48918", long).Read(t.Context(), fakeGet(map[string]string{
		api: `{"title":"Don't change the libraries","body":"Generics are coming.","state":"closed","comments":101,
			"created_at":"2021-10-12T00:00:00Z","user":{"login":"robpike"},"author_association":"CONTRIBUTOR"}`,
		api + "/comments?per_page=100&page=1": comments,
		api + "/comments?per_page=100&page=2": `[{"body":"Last word.","user":{"login":"rsc"},"created_at":"2021-11-01T00:00:00Z","author_association":"MEMBER"}]`,
	}, &asked))
	if err != nil || !strings.HasPrefix(md, "# Don't change the libraries #48918\n\nclosed · opened by robpike (contributor) on 2021-10-12 · 101 comments") ||
		!strings.Contains(md, "### rsc (member) on 2021-11-01\n\nLast word.") || strings.Count(md, "### x on 2021-10-13") != 100 {
		t.Errorf("%v, asked %v:\n%.500s", err, asked, md)
	}
	if _, err := ForPage("https://github.com/a/b/issues/2", long).Read(t.Context(), fakeGet(map[string]string{"https://api.github.com/repos/a/b/issues/2": "{"}, &asked)); err == nil {
		t.Error("a broken API answer")
	}
}

func TestHelpers(t *testing.T) {
	if g := generator([]byte(`<meta name="generator" content="Discourse 3">`)); g != "Discourse 3" {
		t.Errorf("generator %q", g)
	}
	if g := generator([]byte(`<meta name="generator" content="unclosed`)); g != "" {
		t.Errorf("generator %q", g)
	}
	if ForURL("::bad") != nil || ForPage("::bad", nil) != nil {
		t.Error("a bad URL has no route")
	}
}
