package site

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const bskyAPI = "https://public.api.bsky.app/xrpc/"

// Feed and thread sizes: enough to read an account or a conversation, and
// well under fetch.MaxBytes, past which the answer would be cut.
const (
	bskyFeedLimit  = 30
	bskyMaxReplies = 80
)

var bskyPath = regexp.MustCompile(`^/profile/([A-Za-z0-9._:-]+)(?:/post/([A-Za-z0-9._~:-]+))?/?$`)

// bluesky reads a profile or a post through the public AppView, which needs
// no account; the bsky.app pages are an empty JavaScript app.
func bluesky(u *url.URL) *Route {
	m := bskyPath.FindStringSubmatch(u.Path)
	if strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") != "bsky.app" || m == nil {
		return nil
	}
	actor, rkey := m[1], m[2]
	if rkey == "" {
		return &Route{Name: "Bluesky API", Read: func(ctx context.Context, get Getter) (string, error) {
			return bskyProfile(ctx, get, actor)
		}}
	}
	// getPostThreadV2 puts the author's own thread first and limits how far
	// each reply branches; the original getPostThread returns every reply
	// to a fixed depth, so a long thread by its author gets cut.
	api := bskyAPI + "app.bsky.unspecced.getPostThreadV2?anchor=" +
		url.QueryEscape("at://"+actor+"/app.bsky.feed.post/"+rkey) + "&below=10&branchingFactor=3&sort=top"
	return &Route{Name: "Bluesky API", Read: func(ctx context.Context, get Getter) (string, error) {
		body, err := get(ctx, api)
		if err != nil {
			return "", err
		}
		return bskyThread(body)
	}}
}

type bskyLabel struct {
	Val string `json:"val"`
}

type bskyAuthor struct {
	DID         string      `json:"did"`
	Handle      string      `json:"handle"`
	DisplayName string      `json:"displayName"`
	Labels      []bskyLabel `json:"labels"`
}

type bskyRecord struct {
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
	Facets    []struct {
		Index struct {
			Start int `json:"byteStart"`
			End   int `json:"byteEnd"`
		} `json:"index"`
		Features []struct {
			Type string `json:"$type"`
			URI  string `json:"uri"`
		} `json:"features"`
	} `json:"facets"`
}

type bskyPost struct {
	URI     string     `json:"uri"`
	Author  bskyAuthor `json:"author"`
	Record  bskyRecord `json:"record"`
	Embed   *bskyEmbed `json:"embed"`
	Replies int        `json:"replyCount"`
	Reposts int        `json:"repostCount"`
	Likes   int        `json:"likeCount"`
	Quotes  int        `json:"quoteCount"`
}

// bskyEmbed is any embed view: images, video, a link card, a quoted post,
// or a quoted post with media.
type bskyEmbed struct {
	Type   string `json:"$type"`
	Images []struct {
		Alt string `json:"alt"`
	} `json:"images"`
	Alt      string `json:"alt"`
	External *struct {
		URI         string `json:"uri"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"external"`
	Record *bskyQuote `json:"record"`
	Media  *bskyEmbed `json:"media"`
}

// bskyQuote is a quoted record; with media, the quote sits one level down.
type bskyQuote struct {
	Type   string      `json:"$type"`
	URI    string      `json:"uri"`
	Author bskyAuthor  `json:"author"`
	Value  bskyRecord  `json:"value"`
	Embeds []bskyEmbed `json:"embeds"`
	Record *bskyQuote  `json:"record"`
}

func bskyProfile(ctx context.Context, get Getter, actor string) (string, error) {
	body, err := get(ctx, bskyAPI+"app.bsky.actor.getProfile?actor="+url.QueryEscape(actor))
	if err != nil {
		return "", err
	}
	var p struct {
		bskyAuthor
		Description string `json:"description"`
		Followers   int    `json:"followersCount"`
		Follows     int    `json:"followsCount"`
		Posts       int    `json:"postsCount"`
		CreatedAt   string `json:"createdAt"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", fmt.Errorf("decoding its answer: %w", err)
	}
	if signedInOnly(p.bskyAuthor) {
		return "", errors.New("the account shows its posts only to signed-in users")
	}
	body, err = get(ctx, bskyAPI+"app.bsky.feed.getAuthorFeed?actor="+url.QueryEscape(actor)+
		fmt.Sprintf("&limit=%d&filter=posts_and_author_threads", bskyFeedLimit))
	if err != nil {
		return "", err
	}
	var feed struct {
		Feed []struct {
			Post   bskyPost `json:"post"`
			Reason *struct {
				Type string     `json:"$type"`
				By   bskyAuthor `json:"by"`
			} `json:"reason"`
			Reply *struct {
				Parent struct {
					Author bskyAuthor `json:"author"`
				} `json:"parent"`
			} `json:"reply"`
		} `json:"feed"`
	}
	if err := json.Unmarshal(body, &feed); err != nil {
		return "", fmt.Errorf("decoding its answer: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%d followers · %d following · %d posts", bskyName(p.bskyAuthor), p.Followers, p.Follows, p.Posts)
	if day, _, ok := strings.Cut(p.CreatedAt, "T"); ok {
		b.WriteString(" · joined " + day)
	}
	b.WriteString("\n\n" + strings.TrimSpace(p.Description) + "\n")
	if len(feed.Feed) > 0 {
		b.WriteString("\n## Recent posts\n")
	}
	for _, it := range feed.Feed {
		var prefix, reply string
		if it.Reason != nil && strings.HasSuffix(it.Reason.Type, "#reasonRepost") {
			prefix = "Reposted by @" + oneLine(it.Reason.By.Handle) + ": "
		}
		if it.Reply != nil && it.Reply.Parent.Author.Handle != "" {
			reply = "@" + it.Reply.Parent.Author.Handle
		}
		writeBskyPost(&b, prefix, reply, it.Post)
	}
	return strings.TrimSpace(b.String()), nil
}

type bskyItem struct {
	Depth int `json:"depth"`
	Value struct {
		Type string   `json:"$type"`
		Post bskyPost `json:"post"`
	} `json:"value"`
}

func bskyThread(body []byte) (string, error) {
	var res struct {
		Thread []bskyItem `json:"thread"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("decoding its answer: %w", err)
	}
	i := slices.IndexFunc(res.Thread, func(t bskyItem) bool { return t.Depth == 0 })
	if i < 0 {
		return "", errors.New("no such post")
	}
	if missing := bskyMissing(res.Thread[i].Value.Type); missing != "" {
		return "", errors.New(missing)
	}
	anchor := res.Thread[i].Value.Post
	var b strings.Builder
	fmt.Fprintf(&b, "# Bluesky post by %s\n", bskyName(anchor.Author))
	handles := map[int]string{} // by depth, so a reply names the post it answers
	shown := 0
	for j, t := range res.Thread {
		switch {
		case j == 0 && t.Depth < 0:
			b.WriteString("\n## Earlier in the thread\n")
		case t.Depth == 0:
			b.WriteString("\n## The post\n")
		case j > 0 && t.Depth == 1 && res.Thread[j-1].Depth == 0:
			fmt.Fprintf(&b, "\n## Replies (%d)\n", anchor.Replies)
		}
		if t.Depth > 0 && shown == bskyMaxReplies {
			fmt.Fprintf(&b, "\nOnly the first %d replies are shown.\n", bskyMaxReplies)
			break
		}
		if t.Depth > 0 {
			shown++
		}
		if missing := bskyMissing(t.Value.Type); missing != "" {
			handles[t.Depth] = ""
			fmt.Fprintf(&b, "\n### (%s)\n", missing)
			continue
		}
		handles[t.Depth] = t.Value.Post.Author.Handle
		var reply string
		if t.Depth > 1 && handles[t.Depth-1] != "" {
			reply = "@" + handles[t.Depth-1]
		}
		writeBskyPost(&b, "", reply, t.Value.Post)
	}
	return strings.TrimSpace(b.String()), nil
}

// bskyMissing says why a thread item has no post, or "" when it has one.
func bskyMissing(itemType string) string {
	switch _, kind, _ := strings.Cut(itemType, "#"); kind {
	case "threadItemPost":
		return ""
	case "threadItemNotFound":
		return "post not found or deleted"
	case "threadItemBlocked":
		return "post from a blocked account"
	case "threadItemNoUnauthenticated":
		return "the author shows this post only to signed-in users"
	default:
		return "post unavailable"
	}
}

func writeBskyPost(b *strings.Builder, prefix, replyTo string, p bskyPost) {
	head := []string{prefix + bskyName(p.Author)}
	if day, _, ok := strings.Cut(p.Record.CreatedAt, "T"); ok {
		head = append(head, day)
	}
	if replyTo != "" {
		head = append(head, "reply to "+oneLine(replyTo))
	}
	counts := fmt.Sprintf("%d replies, %d reposts, %d likes", p.Replies, p.Reposts, p.Likes)
	if p.Quotes > 0 {
		counts += fmt.Sprintf(", %d quotes", p.Quotes)
	}
	head = append(head, counts)
	if link := bskyLink(p.Author, p.URI); link != "" {
		head = append(head, link)
	}
	fmt.Fprintf(b, "\n### %s\n", strings.Join(head, " · "))
	if signedInOnly(p.Author) {
		b.WriteString("\n(The author shows this post only to signed-in users.)\n")
		return
	}
	if text := bskyText(p.Record); text != "" {
		b.WriteString("\n" + text + "\n")
	}
	if p.Embed != nil {
		writeBskyEmbed(b, *p.Embed, true)
	}
}

func writeBskyEmbed(b *strings.Builder, e bskyEmbed, quotes bool) {
	kind, _, _ := strings.Cut(strings.TrimPrefix(e.Type, "app.bsky.embed."), "#")
	switch kind {
	case "images":
		for _, img := range e.Images {
			b.WriteString("\n" + bskyMedia("image", img.Alt) + "\n")
		}
	case "video":
		b.WriteString("\n" + bskyMedia("video", e.Alt) + "\n")
	case "external":
		if e.External != nil {
			fmt.Fprintf(b, "\nLink: [%s](%s)", cmp.Or(oneLine(e.External.Title), e.External.URI), e.External.URI)
			if d := oneLine(e.External.Description); d != "" {
				b.WriteString(": " + d)
			}
			b.WriteString("\n")
		}
	case "record":
		if quotes && e.Record != nil {
			writeBskyQuote(b, *e.Record)
		}
	case "recordWithMedia":
		if e.Media != nil {
			writeBskyEmbed(b, *e.Media, false)
		}
		if quotes && e.Record != nil && e.Record.Record != nil {
			writeBskyQuote(b, *e.Record.Record)
		}
	}
}

func writeBskyQuote(b *strings.Builder, q bskyQuote) {
	var in strings.Builder
	switch _, kind, _ := strings.Cut(q.Type, "#"); {
	case kind == "viewRecord" && signedInOnly(q.Author):
		in.WriteString("Quoting a post its author shows only to signed-in users\n")
	case kind == "viewRecord":
		fmt.Fprintf(&in, "Quoting %s", bskyName(q.Author))
		if day, _, ok := strings.Cut(q.Value.CreatedAt, "T"); ok {
			in.WriteString(" · " + day)
		}
		if link := bskyLink(q.Author, q.URI); link != "" {
			in.WriteString(" · " + link)
		}
		in.WriteString("\n\n" + bskyText(q.Value) + "\n")
		for _, e := range q.Embeds {
			writeBskyEmbed(&in, e, false)
		}
	case kind == "viewNotFound" || kind == "viewBlocked" || kind == "viewDetached":
		in.WriteString("Quoting a post that is deleted, blocked or detached\n")
	default:
		return
	}
	b.WriteString("\n")
	for line := range strings.Lines(strings.TrimSpace(in.String()) + "\n") {
		b.WriteString(strings.TrimSpace("> "+line) + "\n")
	}
}

func bskyMedia(kind, alt string) string {
	if alt = oneLine(alt); alt != "" {
		return "[" + kind + ": " + alt + "]"
	}
	return "[" + kind + "]"
}

// bskyText is a post's text with its links in full: the text shows a
// shortened address, the link facet holds the whole one.
func bskyText(r bskyRecord) string {
	text, at := r.Text, 0
	type link struct {
		start, end int
		uri        string
	}
	var links []link
	for _, f := range r.Facets {
		for _, ft := range f.Features {
			if strings.HasSuffix(ft.Type, "#link") && (strings.HasPrefix(ft.URI, "https://") || strings.HasPrefix(ft.URI, "http://")) {
				links = append(links, link{f.Index.Start, f.Index.End, ft.URI})
			}
		}
	}
	slices.SortFunc(links, func(a, c link) int { return cmp.Compare(a.start, c.start) })
	var b strings.Builder
	for _, l := range links {
		if l.start < at || l.end > len(text) || l.start >= l.end {
			continue
		}
		b.WriteString(text[at:l.start] + "[" + text[l.start:l.end] + "](" + l.uri + ")")
		at = l.end
	}
	b.WriteString(text[at:])
	return strings.TrimSpace(b.String())
}

func bskyName(a bskyAuthor) string {
	handle := "@" + oneLine(a.Handle)
	if name := oneLine(a.DisplayName); name != "" {
		return name + " (" + handle + ")"
	}
	return handle
}

// bskyLink is a post's bsky.app address, from its at:// URI.
func bskyLink(a bskyAuthor, uri string) string {
	_, rkey, ok := strings.Cut(uri, "/app.bsky.feed.post/")
	if !ok || rkey == "" {
		return ""
	}
	who := a.Handle
	if who == "" || who == "handle.invalid" {
		who = a.DID
	}
	return "https://bsky.app/profile/" + url.PathEscape(who) + "/post/" + url.PathEscape(rkey)
}

// signedInOnly reports the label by which an author asks that logged-out
// readers not see their posts; bsky.app honors it, and so does refetch.
func signedInOnly(a bskyAuthor) bool {
	return slices.ContainsFunc(a.Labels, func(l bskyLabel) bool { return l.Val == "!no-unauthenticated" })
}
