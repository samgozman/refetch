package site

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/samgozman/refetch/fetch"
)

var (
	mastodonProfile = regexp.MustCompile(`^/@([^/@]+(?:@[^/@]+)?)/?$`)
	mastodonPost    = regexp.MustCompile(`^/@[^/]+/(\d+)/?$`)
)

// mastodonPosts is how many recent posts a profile shows.
const mastodonPosts = 20

// mastodon reads a profile or a post through the instance's REST API. The
// page is a JavaScript app: the HTTP clients get an empty shell, and headless
// Chrome takes seconds to fill it. Instances run on any host, so the page's
// markup names them: the app's mount point and its initial-state script. An
// instance in limited mode refuses the API, and the page comes back.
func mastodon(u *url.URL, body []byte) *Route {
	if !bytes.Contains(body, []byte(`id="initial-state"`)) || !bytes.Contains(body, []byte(`id="mastodon"`)) {
		return nil
	}
	m := mastodonAPI{base: &url.URL{Scheme: u.Scheme, Host: u.Host}}
	if p := mastodonPost.FindStringSubmatch(u.Path); p != nil {
		return &Route{Name: "Mastodon API", Read: func(ctx context.Context, get Getter) (string, error) {
			return m.thread(ctx, get, p[1])
		}}
	}
	if p := mastodonProfile.FindStringSubmatch(u.Path); p != nil {
		return &Route{Name: "Mastodon API", Read: func(ctx context.Context, get Getter) (string, error) {
			return m.profile(ctx, get, p[1])
		}}
	}
	return nil
}

type mastodonAPI struct{ base *url.URL }

func (m mastodonAPI) get(ctx context.Context, get Getter, path string, v any) error {
	raw, err := get(ctx, m.base.String()+path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decoding its answer: %w", err)
	}
	return nil
}

func (m mastodonAPI) profile(ctx context.Context, get Getter, acct string) (string, error) {
	var a mAccount
	if err := m.get(ctx, get, "/api/v1/accounts/lookup?acct="+url.QueryEscape(acct), &a); err != nil {
		return "", err
	}
	if a.ID == "" {
		return "", errors.New("no such account")
	}
	var posts []mStatus
	if err := m.get(ctx, get, fmt.Sprintf("/api/v1/accounts/%s/statuses?limit=%d&exclude_replies=true", url.PathEscape(a.ID), mastodonPosts), &posts); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%s)\n\n", a.name(), a.handle(m.base.Host))
	facts := []string{fmt.Sprintf("%d posts · %d following · %d followers", a.Statuses, a.Following, a.Followers)}
	if day, _, ok := strings.Cut(a.Created, "T"); ok {
		facts = append(facts, "joined "+day)
	}
	if a.Bot {
		facts = append(facts, "bot")
	}
	b.WriteString(strings.Join(facts, " · ") + "\n\n")
	if note := m.markdown(a.Note); note != "" {
		b.WriteString(note + "\n\n")
	}
	for _, f := range a.Fields {
		fmt.Fprintf(&b, "- %s: %s\n", oneLine(f.Name), oneLine(m.markdown(f.Value)))
	}
	fmt.Fprintf(&b, "\n## Recent posts, replies left out (%d)\n", len(posts))
	for _, s := range posts {
		m.post(&b, s, nil)
	}
	return strings.TrimSpace(b.String()), nil
}

func (m mastodonAPI) thread(ctx context.Context, get Getter, id string) (string, error) {
	var s mStatus
	if err := m.get(ctx, get, "/api/v1/statuses/"+id, &s); err != nil {
		return "", err
	}
	var c struct {
		Ancestors   []mStatus `json:"ancestors"`
		Descendants []mStatus `json:"descendants"`
	}
	if err := m.get(ctx, get, "/api/v1/statuses/"+id+"/context", &c); err != nil {
		return "", err
	}
	authors := map[string]string{}
	for _, t := range append(append(c.Ancestors, s), c.Descendants...) {
		authors[t.ID] = t.Account.handle(m.base.Host)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Post by %s (%s)\n", s.Account.name(), s.Account.handle(m.base.Host))
	if len(c.Ancestors) > 0 {
		b.WriteString("\n## Earlier in the thread\n")
		for _, t := range c.Ancestors {
			m.post(&b, t, authors)
		}
		b.WriteString("\n## The post\n")
	}
	m.post(&b, s, authors)
	if len(c.Descendants) > 0 {
		fmt.Fprintf(&b, "\n## Replies (%d)\n", len(c.Descendants))
		for _, t := range c.Descendants {
			m.post(&b, t, authors)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// post writes one status; authors, when given, names the post a reply
// answers.
func (m mastodonAPI) post(b *strings.Builder, s mStatus, authors map[string]string) {
	head := fmt.Sprintf("%s (%s)", s.Account.name(), s.Account.handle(m.base.Host))
	if s.Reblog != nil {
		head += " boosted " + fmt.Sprintf("%s (%s)", s.Reblog.Account.name(), s.Reblog.Account.handle(m.base.Host))
		s = *s.Reblog
	}
	if day, clock, ok := strings.Cut(s.Created, "T"); ok {
		head += " · " + day + " " + clock[:min(len(clock), 5)] + " UTC"
	}
	if to, ok := authors[s.InReplyTo]; ok {
		head += " · replying to " + to
	}
	fmt.Fprintf(b, "\n### %s\n\n", head)
	if cw := oneLine(s.Spoiler); cw != "" {
		fmt.Fprintf(b, "Content warning: %s\n\n", cw)
	}
	if text := m.markdown(s.Content); text != "" {
		b.WriteString(text + "\n\n")
	}
	if s.Poll != nil {
		var opts []string
		for _, o := range s.Poll.Options {
			opts = append(opts, fmt.Sprintf("%s (%d)", oneLine(o.Title), o.Votes))
		}
		fmt.Fprintf(b, "Poll: %s\n\n", strings.Join(opts, " · "))
	}
	for _, a := range s.Media {
		fmt.Fprintf(b, "- %s %s: %s\n", a.Type, a.URL, cmp.Or(oneLine(a.Description), "(no description)"))
	}
	if len(s.Media) > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%d replies · %d boosts · %d favourites · %s\n", s.Replies, s.Reblogs, s.Favourites, s.URL)
}

// markdown turns a status's or a profile's HTML into Markdown.
func (m mastodonAPI) markdown(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	return strings.TrimSpace(fetch.FullMarkdown([]byte(h), "text/html", m.base))
}

type mAccount struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name"`
	Note        string `json:"note"`
	Created     string `json:"created_at"`
	Bot         bool   `json:"bot"`
	Statuses    int    `json:"statuses_count"`
	Following   int    `json:"following_count"`
	Followers   int    `json:"followers_count"`
	Fields      []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"fields"`
}

func (a mAccount) name() string {
	if n := oneLine(a.DisplayName); n != "" {
		return n
	}
	return oneLine(a.Username)
}

// handle is the account's full address; the API leaves the host off local
// accounts.
func (a mAccount) handle(host string) string {
	acct := oneLine(a.Acct)
	if !strings.Contains(acct, "@") {
		acct += "@" + host
	}
	return "@" + acct
}

type mStatus struct {
	ID         string   `json:"id"`
	Created    string   `json:"created_at"`
	InReplyTo  string   `json:"in_reply_to_id"`
	Spoiler    string   `json:"spoiler_text"`
	Content    string   `json:"content"`
	URL        string   `json:"url"`
	Replies    int      `json:"replies_count"`
	Reblogs    int      `json:"reblogs_count"`
	Favourites int      `json:"favourites_count"`
	Account    mAccount `json:"account"`
	Reblog     *mStatus `json:"reblog"`
	Media      []struct {
		Type        string `json:"type"`
		URL         string `json:"url"`
		Description string `json:"description"`
	} `json:"media_attachments"`
	Poll *struct {
		Options []struct {
			Title string `json:"title"`
			Votes int    `json:"votes_count"`
		} `json:"options"`
	} `json:"poll"`
}
