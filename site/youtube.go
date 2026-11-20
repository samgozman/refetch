package site

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var youtubeShorts = regexp.MustCompile(`^/shorts/([\w-]+)/?$`)

// youtube reads a video page from the player data it carries for its
// scripts: the HTML around it holds only the title and the footer, and a
// browser's render only the player's controls. The data is in the page, so
// the route asks for nothing more. A page without it (a consent wall, a
// removed video) has no route and comes back as it is.
func youtube(u *url.URL, body []byte) *Route {
	id := youtubeID(u)
	if id == "" {
		return nil
	}
	var pr ytPlayer
	if !playerResponse(body, &pr) || pr.Video.ID != id {
		return nil
	}
	md := pr.markdown()
	return &Route{Name: "YouTube page data", Read: func(context.Context, Getter) (string, error) {
		return md, nil
	}}
}

func youtubeID(u *url.URL) string {
	switch strings.ToLower(u.Hostname()) {
	case "youtu.be":
		return strings.Trim(u.Path, "/")
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if u.Path == "/watch" {
			return u.Query().Get("v")
		}
		if m := youtubeShorts.FindStringSubmatch(u.Path); m != nil {
			return m[1]
		}
	}
	return ""
}

// playerResponse decodes the one JSON value after the assignment; a script
// statement follows it, so the rest of the page is not JSON.
func playerResponse(body []byte, pr *ytPlayer) bool {
	_, rest, ok := bytes.Cut(body, []byte("ytInitialPlayerResponse = "))
	if !ok {
		return false
	}
	v, err := jsontext.NewDecoder(bytes.NewReader(rest)).ReadValue()
	return err == nil && json.Unmarshal(v, pr) == nil
}

type ytPlayer struct {
	Playability struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	Video struct {
		ID          string   `json:"videoId"`
		Title       string   `json:"title"`
		Author      string   `json:"author"`
		ChannelID   string   `json:"channelId"`
		Description string   `json:"shortDescription"`
		Seconds     string   `json:"lengthSeconds"`
		Views       string   `json:"viewCount"`
		Keywords    []string `json:"keywords"`
	} `json:"videoDetails"`
	Microformat struct {
		Player struct {
			Published string `json:"publishDate"`
			Uploaded  string `json:"uploadDate"`
			Category  string `json:"category"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
}

func (pr ytPlayer) markdown() string {
	v, mf := pr.Video, pr.Microformat.Player
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(v.Title))
	facts := []string{fmt.Sprintf("Channel: [%s](https://www.youtube.com/channel/%s)", oneLine(v.Author), url.PathEscape(v.ChannelID))}
	if day, _, _ := strings.Cut(cmp.Or(mf.Published, mf.Uploaded), "T"); day != "" {
		facts = append(facts, "published "+day)
	}
	if s, _ := strconv.Atoi(v.Seconds); s > 0 {
		facts = append(facts, clock(s))
	}
	if n, err := strconv.ParseInt(v.Views, 10, 64); err == nil {
		facts = append(facts, thousands(n)+" views")
	}
	if mf.Category != "" {
		facts = append(facts, oneLine(mf.Category))
	}
	b.WriteString(strings.Join(facts, " · ") + "\n\n")
	if len(v.Keywords) > 0 {
		fmt.Fprintf(&b, "Keywords: %s\n\n", oneLine(strings.Join(v.Keywords, ", ")))
	}
	if st := pr.Playability.Status; st != "" && st != "OK" {
		fmt.Fprintf(&b, "Not playable here: %s\n\n", oneLine(strings.TrimSpace(st+" "+pr.Playability.Reason)))
	}
	b.WriteString("No transcript: YouTube serves captions only with a token refetch does not have.\n")
	if d := strings.TrimSpace(v.Description); d != "" {
		fmt.Fprintf(&b, "\n## Description\n\n%s\n", d)
	}
	return strings.TrimSpace(b.String())
}

// clock is a duration as YouTube shows it: 3:07, 1:02:03.
func clock(s int) string {
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
