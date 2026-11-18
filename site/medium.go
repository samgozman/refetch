package site

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samgozman/refetch/fetch"
)

// mediumPost matches a post's path: an optional @user or publication, then
// the slug ending in the post id.
var mediumPost = regexp.MustCompile(`^/(?:([^/]+)/)?(?:[^/]*-)?([0-9a-f]{10,16})/?$`)

// mediumReserved are first path segments that name no user or publication.
var mediumReserved = []string{"p", "m", "tag", "tags", "topic", "topics", "search", "me", "feed", "_", "plans", "membership", "creators", "about"}

// medium reads a post from its author's or publication's RSS feed, which
// carries the newest posts in full while Medium's pages challenge every
// automated reader. A medium.com/p/<id> address names neither, so it gets
// no route.
func medium(u *url.URL) *Route {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	m := mediumPost.FindStringSubmatch(u.Path)
	if m == nil {
		return nil
	}
	var feed string
	switch sub, ok := strings.CutSuffix(host, ".medium.com"); {
	case host == "medium.com" && m[1] != "" && !slices.Contains(mediumReserved, m[1]):
		feed = "https://medium.com/feed/" + url.PathEscape(m[1])
	case ok && m[1] == "" && !strings.Contains(sub, "."):
		// The subdomain's own feed: medium.com/feed/@<sub> can be another
		// account, as for a publication's subdomain.
		feed = "https://" + sub + ".medium.com/feed"
	default:
		return nil
	}
	id := m[2]
	return &Route{Name: "Medium RSS feed", Read: func(ctx context.Context, get Getter) (string, error) {
		body, err := get(ctx, feed)
		if err != nil {
			return "", err
		}
		return mediumMarkdown(body, id)
	}}
}

type mediumItem struct {
	Title      string   `xml:"title"`
	Link       string   `xml:"link"`
	GUID       string   `xml:"guid"`
	Creator    string   `xml:"http://purl.org/dc/elements/1.1/ creator"`
	PubDate    string   `xml:"pubDate"`
	Categories []string `xml:"category"`
	Content    string   `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
}

func mediumMarkdown(body []byte, id string) (string, error) {
	var rss struct {
		Items []mediumItem `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil {
		return "", fmt.Errorf("decoding the feed: %w", err)
	}
	i := slices.IndexFunc(rss.Items, func(it mediumItem) bool {
		link, _, _ := strings.Cut(it.Link, "?")
		return path.Base(it.GUID) == id || strings.HasSuffix(link, "-"+id) || strings.HasSuffix(link, "/"+id)
	})
	if i < 0 {
		return "", errors.New("the feed holds only the newest posts, and not this one")
	}
	it := rss.Items[i]
	if strings.TrimSpace(it.Content) == "" {
		return "", errors.New("the feed has only a preview of this post, as for a member-only story")
	}
	meta := []string{"by " + oneLine(it.Creator)}
	if t, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
		meta = append(meta, t.UTC().Format(time.DateOnly))
	}
	if len(it.Categories) > 0 {
		meta = append(meta, "tags: "+oneLine(strings.Join(it.Categories, ", ")))
	}
	base, _ := url.Parse(it.Link)
	md := fetch.FullMarkdown([]byte(it.Content), "text/html", base)
	return fmt.Sprintf("# %s\n\n%s\n\n%s", oneLine(it.Title), strings.Join(meta, " · "), strings.TrimSpace(md)), nil
}
