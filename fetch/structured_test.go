package fetch

import (
	"strings"
	"testing"
)

func ldPage(ld, body string) []byte {
	return []byte(`<html><head><title>Q</title><script type="application/ld+json">` + ld + `</script></head><body>` + body + `</body></html>`)
}

func TestStructured(t *testing.T) {
	answer := strings.Repeat("Restart the print spooler service, then clear the queue. ", 4)
	qa := `{"@context":"https://schema.org","@type":"QAPage","mainEntity":{"@type":"Question","name":"Printer stuck","text":"<p>My printer <b>queue</b> is stuck.</p>",
		"author":{"@type":"Person","name":"ada"},"dateCreated":"2026-10-01T10:00:00Z",
		"acceptedAnswer":{"@type":"Answer","text":"` + answer + `","author":{"name":"bob"},"upvoteCount":12},
		"suggestedAnswer":[{"@type":"Answer","text":"Buy a new one.","author":"cy"}]}}`
	md := Structured(ldPage(qa, `<div id="root"></div>`), nil)
	for _, w := range []string{StructuredLabel, "# Printer stuck", "by ada · 2026-10-01", "My printer **queue** is stuck.", "## Answers", "by bob · 12 votes", "Restart the print spooler", "by cy", "Buy a new one."} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}

	forum := `[{"@type":"Organization","name":"x"},{"@graph":[{"@type":["DiscussionForumPosting"],"headline":"Sync fails","articleBody":"` + answer + `",
		"comment":[{"@type":"Comment","text":"Same here.","author":{"name":"di"}}]}]}]`
	if md := Structured(ldPage(forum, ""), nil); !strings.Contains(md, "# Sync fails") || !strings.Contains(md, "## Comments") || !strings.Contains(md, "Same here.") {
		t.Errorf("forum post:\n%s", md)
	}
	for name, ld := range map[string]string{
		"teaser":  `{"@type":"Article","headline":"News","articleBody":"Short teaser."}`,
		"broken":  `{"@type":`,
		"no post": `{"@type":"Organization","name":"Acme","description":"` + answer + `"}`,
	} {
		if md := Structured(ldPage(ld, ""), nil); md != "" {
			t.Errorf("%s: %q", name, md)
		}
	}

	shell := ldPage(qa, `<div id="root"></div><p>Sign in</p>`)
	if md := Markdown(shell, "text/html", nil); !strings.Contains(md, StructuredLabel) || !strings.Contains(md, "Restart the print spooler") {
		t.Errorf("a thin page falls back to its structured data:\n%s", md)
	}
	article := ldPage(qa, `<article><h1>Printer stuck</h1><p>`+strings.Repeat("The page's own long text. ", 20)+`</p></article>`)
	if md := Markdown(article, "text/html", nil); strings.Contains(md, StructuredLabel) {
		t.Errorf("a page with its own text keeps it:\n%s", md)
	}
}
