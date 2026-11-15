package site

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// discourseTopic is a Discourse topic path: /t/slug/123, /t/123, with a
// post number or under a subfolder install (/forum/t/...).
var discourseTopic = regexp.MustCompile(`^(.*?)/t/(?:[^/]+/)?(\d+)(?:/\d+)?/?$`)

// maxRawPages caps a topic's /raw pages, about 100 posts each.
const maxRawPages = 5

// discourse reads a topic from /raw/<id>, every post's Markdown source with
// its author and date, where the page shows a browser only the first posts
// until scripts load the rest.
func discourse(u *url.URL, body []byte) *Route {
	m := discourseTopic.FindStringSubmatch(u.Path)
	if m == nil || !strings.HasPrefix(generator(body), "Discourse") {
		return nil
	}
	raw := u.Scheme + "://" + u.Host + m[1] + "/raw/" + m[2]
	title := pageTitle(body)
	return &Route{Name: "Discourse /raw", Read: func(ctx context.Context, get Getter) (string, error) {
		var b strings.Builder
		if title != "" {
			fmt.Fprintf(&b, "# %s\n\n", title)
		}
		for page := 1; page <= maxRawPages; page++ {
			text, err := get(ctx, fmt.Sprintf("%s?page=%d", raw, page))
			if err != nil {
				if page == 1 {
					return "", err
				}
				break
			}
			if strings.TrimSpace(string(text)) == "" {
				break
			}
			b.Write(text)
			b.WriteString("\n")
		}
		if b.Len() == 0 {
			return "", fmt.Errorf("topic %s is empty", m[2])
		}
		return strings.TrimSpace(b.String()), nil
	}}
}
