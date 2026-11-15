package fetch

import (
	"net/url"
	"strings"
)

// Rewrite returns the address that serves a page's content directly, or ""
// when rawURL is read as is. Google Docs, Sheets and Slides editors are
// JavaScript apps whose HTML holds no document text, and a Drive file's
// page is a viewer: their export addresses return the document itself, for
// anyone the document is shared with.
func Rewrite(rawURL string) string {
	u, err := parseURL(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch u.Hostname() {
	case "docs.google.com":
		// /document/d/<id>/edit; /document/d/e/<id>/pub is already a
		// published HTML page.
		if len(parts) < 3 || parts[1] != "d" || parts[2] == "e" || parts[2] == "" {
			return ""
		}
		id := url.PathEscape(parts[2])
		switch parts[0] {
		case "document":
			return "https://docs.google.com/document/d/" + id + "/export?format=html"
		case "presentation":
			return "https://docs.google.com/presentation/d/" + id + "/export?format=pptx"
		case "spreadsheets":
			out := "https://docs.google.com/spreadsheets/d/" + id + "/gviz/tq?tqx=out:html"
			if gid := sheetTab(u); gid != "" {
				out += "&gid=" + url.QueryEscape(gid)
			}
			return out
		}
	case "drive.google.com":
		id := ""
		switch {
		case len(parts) >= 3 && parts[0] == "file" && parts[1] == "d":
			id = parts[2]
		case len(parts) == 1 && parts[0] == "open":
			id = u.Query().Get("id")
		}
		if id != "" {
			return "https://drive.google.com/uc?export=download&id=" + url.QueryEscape(id)
		}
	}
	return ""
}

// sheetTab is the tab a Sheets link points at: gid in the query or in the
// fragment (#gid=123).
func sheetTab(u *url.URL) string {
	if gid := u.Query().Get("gid"); gid != "" {
		return gid
	}
	if q, err := url.ParseQuery(u.Fragment); err == nil {
		return q.Get("gid")
	}
	return ""
}
