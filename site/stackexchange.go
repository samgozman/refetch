package site

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// seFilter asks for a question with its body, answers and comments as
// their Markdown source, and who wrote them. Filters never expire; this one
// was made with /2.3/filters/create.
const seFilter = "!)PiAlwRxgCI.Y8TjE4(kd0i(uOnH4D6lbuV0GICG8wNXvaML4iKA"

var seQuestion = regexp.MustCompile(`^/(?:questions|q)/(\d+)(?:/|$)`)

// seHosts are the Stack Exchange sites outside *.stackexchange.com.
var seHosts = []string{"stackoverflow.com", "superuser.com", "serverfault.com", "askubuntu.com", "mathoverflow.net", "stackapps.com"}

// stackExchange reads a question through the Stack Exchange API, which
// answers where the sites' Cloudflare challenge stops automated readers;
// anonymous use allows 300 requests a day per IP address.
func stackExchange(u *url.URL) *Route {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	known := slices.Contains(seHosts, host) || strings.HasSuffix(host, ".stackoverflow.com") ||
		(strings.HasSuffix(host, ".stackexchange.com") && !slices.Contains([]string{"api.stackexchange.com", "chat.stackexchange.com"}, host))
	m := seQuestion.FindStringSubmatch(u.Path)
	if !known || m == nil {
		return nil
	}
	api := "https://api.stackexchange.com/2.3/questions/" + m[1] + "?site=" + url.QueryEscape(host) + "&filter=" + url.QueryEscape(seFilter)
	return &Route{Name: "Stack Exchange API", Read: func(ctx context.Context, get Getter) (string, error) {
		body, err := get(ctx, api)
		if err != nil {
			return "", err
		}
		return seMarkdown(body)
	}}
}

type seUser struct {
	Name string `json:"display_name"`
}

type sePost struct {
	Title    string      `json:"title"`
	Body     string      `json:"body_markdown"`
	Score    int         `json:"score"`
	Accepted bool        `json:"is_accepted"`
	Owner    seUser      `json:"owner"`
	Created  int64       `json:"creation_date"`
	Link     string      `json:"link"`
	Tags     []string    `json:"tags"`
	Closed   string      `json:"closed_reason"`
	Comments []seComment `json:"comments"`
	Answers  []sePost    `json:"answers"`
}

type seComment struct {
	Body  string `json:"body_markdown"`
	Owner seUser `json:"owner"`
	Score int    `json:"score"`
}

func seMarkdown(body []byte) (string, error) {
	var res struct {
		Items        []sePost `json:"items"`
		ErrorMessage string   `json:"error_message"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("decoding its answer: %w", err)
	}
	if res.ErrorMessage != "" {
		return "", errors.New(oneLine(res.ErrorMessage))
	}
	if len(res.Items) == 0 {
		return "", errors.New("no such question")
	}
	q := res.Items[0]
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(q.Title))
	meta := []string{fmt.Sprintf("score %d", q.Score), "asked by " + byline(q.Owner, q.Created)}
	if len(q.Tags) > 0 {
		meta = append(meta, "tags: "+strings.Join(q.Tags, ", "))
	}
	if q.Closed != "" {
		meta = append(meta, "closed: "+oneLine(q.Closed))
	}
	b.WriteString(strings.Join(meta, " · ") + "\n\n" + html.UnescapeString(q.Body) + "\n")
	writeComments(&b, q.Comments)
	slices.SortStableFunc(q.Answers, func(a, c sePost) int {
		if a.Accepted != c.Accepted {
			if a.Accepted {
				return -1
			}
			return 1
		}
		return cmp.Compare(c.Score, a.Score)
	})
	if len(q.Answers) > 0 {
		fmt.Fprintf(&b, "\n## %d answers\n", len(q.Answers))
	}
	for _, a := range q.Answers {
		label := "Answer"
		if a.Accepted {
			label = "Accepted answer"
		}
		fmt.Fprintf(&b, "\n### %s · score %d · %s\n\n%s\n", label, a.Score, byline(a.Owner, a.Created), html.UnescapeString(a.Body))
		writeComments(&b, a.Comments)
	}
	return strings.TrimSpace(b.String()), nil
}

func byline(u seUser, created int64) string {
	name := cmp.Or(oneLine(u.Name), "a deleted user")
	if created == 0 {
		return name
	}
	return name + " on " + time.Unix(created, 0).UTC().Format(time.DateOnly)
}

func writeComments(b *strings.Builder, comments []seComment) {
	if len(comments) == 0 {
		return
	}
	b.WriteString("\nComments:\n")
	for _, c := range comments {
		fmt.Fprintf(b, "- %s (%s, score %d)\n", oneLine(c.Body), cmp.Or(oneLine(c.Owner.Name), "a deleted user"), c.Score)
	}
}
