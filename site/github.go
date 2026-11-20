package site

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var githubIssue = regexp.MustCompile(`^/([^/]+)/([^/]+)/(?:issues|pull)/(\d+)/?$`)

// maxCommentPages caps an issue's comment pages, 100 comments each.
const maxCommentPages = 3

// github reads an issue or pull request through the REST API when its page
// hides part of the conversation behind "Load more" (long threads). The
// API allows 60 anonymous requests an hour per IP address, so a short
// thread, whose page holds everything, is read from the page.
func github(u *url.URL, body []byte) *Route {
	m := githubIssue.FindStringSubmatch(u.Path)
	if !strings.EqualFold(u.Hostname(), "github.com") || m == nil || !bytes.Contains(body, []byte("issue-timeline-load-more")) {
		return nil
	}
	api := "https://api.github.com/repos/" + url.PathEscape(m[1]) + "/" + url.PathEscape(m[2]) + "/issues/" + m[3]
	return &Route{Name: "GitHub API", Read: func(ctx context.Context, get Getter) (string, error) {
		raw, err := get(ctx, api)
		if err != nil {
			return "", ghError(raw, err)
		}
		var issue ghPost
		if err := json.Unmarshal(raw, &issue); err != nil {
			return "", fmt.Errorf("decoding its answer: %w", err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s #%s\n\n%s · opened by %s · %d comments\n\n%s\n", oneLine(issue.Title), m[3],
			issue.State, issue.byline(), issue.Comments, strings.TrimSpace(issue.Body))
		if issue.Comments > 0 {
			b.WriteString("\n## Comments\n")
		}
		for page := 1; page <= maxCommentPages; page++ {
			raw, err := get(ctx, fmt.Sprintf("%s/comments?per_page=100&page=%d", api, page))
			if err != nil {
				return "", ghError(raw, err)
			}
			var comments []ghPost
			if err := json.Unmarshal(raw, &comments); err != nil {
				return "", fmt.Errorf("decoding its answer: %w", err)
			}
			for _, c := range comments {
				fmt.Fprintf(&b, "\n### %s\n\n%s\n", c.byline(), strings.TrimSpace(c.Body))
			}
			if len(comments) < 100 {
				break
			}
		}
		return strings.TrimSpace(b.String()), nil
	}}
}

// ghError names a spent rate limit, primary or secondary, which the API
// answers with HTTP 403 or 429 and a message saying so. That message names
// refetch's IP address, so only the getter's error goes on.
func ghError(body []byte, err error) error {
	var res struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &res) == nil && strings.Contains(strings.ToLower(res.Message), "rate limit") {
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	}
	return err
}

type ghPost struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	State    string `json:"state"`
	Comments int    `json:"comments"`
	Created  string `json:"created_at"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	Association string `json:"author_association"`
}

func (p ghPost) byline() string {
	s := oneLine(p.User.Login)
	if a := p.Association; a != "" && a != "NONE" {
		s += " (" + strings.ToLower(a) + ")"
	}
	if day, _, ok := strings.Cut(p.Created, "T"); ok {
		s += " on " + day
	}
	return s
}
