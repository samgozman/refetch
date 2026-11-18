package site

import (
	"os"
	"strings"
	"testing"
)

func TestYouTube(t *testing.T) {
	page, err := os.ReadFile("testdata/youtube_watch.html")
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]bool{
		"https://www.youtube.com/watch?v=jNQXAC9IVRw":                    true,
		"https://m.youtube.com/watch?app=desktop&v=jNQXAC9IVRw":          true,
		"https://youtube.com/shorts/jNQXAC9IVRw":                         true,
		"https://youtu.be/jNQXAC9IVRw":                                   true,
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                    false,
		"https://www.youtube.com/@jawed":                                 false,
		"https://www.youtube.com/results?search_query=zoo&v=jNQXAC9IVRw": false,
		"https://www.example.com/watch?v=jNQXAC9IVRw":                    false,
	} {
		if got := ForPage(raw, page) != nil; got != want {
			t.Errorf("ForPage(%s) = %v", raw, got)
		}
	}
	r := ForPage("https://www.youtube.com/watch?v=jNQXAC9IVRw", page)
	md, err := r.Read(t.Context(), nil)
	want := []string{
		"# Me at the zoo\n\n",
		"Channel: [jawed](https://www.youtube.com/channel/UC4QobU6STFB0P71PMvOGN5A) · published 2005-04-23 · 0:19 · 448,777,225 views · Film & Animation\n",
		"Keywords: me at the zoo, jawed karim, first youtube video",
		"No transcript",
		"## Description\n\nMicroplastics are accumulating",
		"00:17 End",
	}
	for _, w := range want {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}
	if err != nil || r.Name != "YouTube page data" || strings.Contains(md, "Not playable") {
		t.Errorf("%v, %s:\n%s", err, r.Name, md)
	}
}

func TestYouTubeNoPlayerData(t *testing.T) {
	const u = "https://www.youtube.com/watch?v=abcdefghijk"
	for name, body := range map[string]string{
		"consent page": `<html><title>Before you continue to YouTube</title></html>`,
		"broken JSON":  `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"abcdefghijk"</script>`,
		"no video":     `<script>var ytInitialPlayerResponse = {"playabilityStatus":{"status":"ERROR","reason":"Video unavailable"}};</script>`,
	} {
		if ForPage(u, []byte(body)) != nil {
			t.Errorf("%s: a route", name)
		}
	}
	login := `<script>var ytInitialPlayerResponse = {"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm your age"},
		"videoDetails":{"videoId":"abcdefghijk","title":"Grown-up\ntalk","author":"ada","lengthSeconds":"3723","viewCount":"x","isLiveContent":false}};var a = 1;</script>`
	md, err := ForPage(u, []byte(login)).Read(t.Context(), nil)
	for _, w := range []string{"# Grown-up talk\n", "· 1:02:03\n", "Not playable here: LOGIN_REQUIRED Sign in to confirm your age"} {
		if err != nil || !strings.Contains(md, w) {
			t.Errorf("lacks %q: %v\n%s", w, err, md)
		}
	}
	if strings.Contains(md, "views") || strings.Contains(md, "## Description") {
		t.Errorf("no views or description to show:\n%s", md)
	}
}

func TestClock(t *testing.T) {
	for s, want := range map[int]string{19: "0:19", 187: "3:07", 3600: "1:00:00", 36000: "10:00:00"} {
		if got := clock(s); got != want {
			t.Errorf("clock(%d) = %q, want %q", s, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 448777225: "448,777,225"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
