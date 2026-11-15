package fetch

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// furniture are class and id names of site navigation, banners, footers,
// sidebars, ads and cookie notices, matched whole: "ad" is not "add-to-cart".
var furniture = map[string]bool{
	"header": true, "site-header": true, "footer": true, "site-footer": true, "navbar": true, "nav": true,
	"navigation": true, "menu": true, "sidebar": true, "aside": true, "breadcrumb": true, "breadcrumbs": true,
	"modal": true, "popup": true, "overlay": true, "ad": true, "ads": true, "advert": true, "advertisement": true,
	"social": true, "social-links": true, "social-media": true, "share": true, "sharing": true, "widget": true,
	"cookie": true, "cookies": true, "cookie-banner": true, "cookie-consent": true, "cookie-notice": true,
	"lang-selector": true, "language": true, "language-selector": true, "skip-link": true,
}

// pageRoles are ARIA landmarks that hold the site's furniture.
var pageRoles = map[string]bool{"banner": true, "navigation": true, "contentinfo": true, "complementary": true}

// markBoilerplate fills drop with the site's furniture under n and reports
// whether n is or holds the content: <main>, <article>, role="main", an
// element named main, or the <h1>. Furniture that holds the content stays,
// and so does a <header> or <footer> inside an article, which is the
// article's own byline or notes.
func (m *markdown) markBoilerplate(n *html.Node, inArticle bool) bool {
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return false
	}
	content := n.DataAtom == atom.Main || n.DataAtom == atom.Article || n.DataAtom == atom.H1 ||
		attr(n, "role") == "main" || strings.EqualFold(attr(n, "id"), "main")
	inner := inArticle || n.DataAtom == atom.Main || n.DataAtom == atom.Article
	holds := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		holds = m.markBoilerplate(c, inner) || holds
	}
	if !content && !holds && isFurniture(n, inArticle) {
		m.drop[n] = true
	}
	return content || holds
}

func isFurniture(n *html.Node, inArticle bool) bool {
	switch n.DataAtom {
	case atom.Nav, atom.Aside:
		return true
	case atom.Header, atom.Footer:
		return !inArticle
	case atom.Html, atom.Body:
		return false
	case atom.A: // "Skip to content": a keyboard shortcut to the content, not part of it
		return strings.HasPrefix(attr(n, "href"), "#") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(textOf(n))), "skip to")
	default:
	}
	if role := attr(n, "role"); pageRoles[role] {
		return !inArticle || role == "navigation" || role == "complementary"
	}
	if strings.Contains(strings.ToLower(attr(n, "aria-label")), "cookie") {
		return true
	}
	names := append(strings.Fields(attr(n, "class")), attr(n, "id"))
	for _, name := range names {
		name = strings.ToLower(name)
		if furniture[name] && (!inArticle || !articleOwn[name]) {
			return true
		}
	}
	return false
}

// articleOwn are furniture names that, inside an article, are the
// article's own header or footer.
var articleOwn = map[string]bool{"header": true, "footer": true}
