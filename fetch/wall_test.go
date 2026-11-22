package fetch

import (
	"strings"
	"testing"
)

func TestWalls(t *testing.T) {
	article := "<article><h1>How the probe works</h1>" +
		strings.Repeat("<p>The probe measures the field once a second and sends the readings home.</p>", 30) + "</article>"
	cases := []struct {
		name, body, contentType string
		login, notFound         bool
	}{
		{name: "facebook login form", login: true, body: `<html><head><title>NASA - National Aeronautics and Space Administration | Facebook</title></head>
			<body><h2>See more from NASA</h2><form><input type="text" name="email" placeholder="Email or phone number">
			<input type="password" name="pass" placeholder="Password"><button>Log In</button><a href="/recover">Forgot account?</a></form>
			<div aria-hidden="true"><div><h2>This page isn't available right now</h2></div></div></body></html>`},
		{name: "upper-case password type", login: true, body: `<form><INPUT TYPE=PASSWORD name=p></form>`},
		{name: "article with a header login", body: `<html><header><form><input name="u"><input type="password" name="p"></form></header>` + article + `</html>`},
		{name: "password markup inside a script", body: `<html><body><p>Hello.</p><script>tpl = '<input type="password">'</script></body></html>`},
		{name: "reddit soft 404", notFound: true, body: `<html><head><title>Reddit - The heart of the internet</title></head>
			<body><h1>Page not found</h1><p>The page you are looking for does not exist.</p><a href="/">Go home</a></body></html>`},
		{name: "curly apostrophe", notFound: true, body: `<title>Instagram</title><h2>Sorry, this page isn’t available.</h2>`},
		{name: "x soft 404", notFound: true, body: `<title>X</title><h1>Hmm...this page doesn't exist. Try searching for something else.</h1>`},
		{name: "404 title", notFound: true, body: `<html><head><title>404 Not Found</title></head><body><center>nginx</center></body></html>`},
		{name: "site then 404", notFound: true, body: `<title>Example Shop | Page Not Found</title><p>Try the search.</p>`},
		{name: "wordpress", notFound: true, body: `<h1>Oops! That page can’t be found.</h1>`},
		{name: "tiktok", notFound: true, body: `<h2>Couldn't find this account</h2>`},
		{name: "phrase in passing", body: `<html><head><title>Custom error pages</title></head><body><h1>Custom error pages</h1>
			<p>When a visitor sees page not found, nginx serves 404.html instead. This content isn't available elsewhere.</p></body></html>`},
		{name: "phrase mid-title", body: `<title>Fixing page not found errors in nginx</title><p>Check root.</p>`},
		{name: "404 inside a word", body: `<title>Route 404 timetable</title><p>Buses every ten minutes.</p>`},
		{name: "error 404", notFound: true, body: `<title>Error 404</title><p>Gone.</p>`},
		{name: "site then 404", notFound: true, body: `<title>Example - 404</title><p>Gone.</p>`},
		{name: "404 then site", notFound: true, body: `<title>404 | Example</title><p>Gone.</p>`},
		{name: "404 page heading", notFound: true, body: `<h1>404 page</h1><p>Gone.</p>`},
		{name: "a site named 404", body: `<title>404 Media</title><p>Subscribe to read.</p>`},
		{name: "a version number", body: `<title>Release 2.404</title><p>Bug fixes.</p>`},
		{name: "404 explained", body: `<title>Status codes: 404 and 410 explained</title><p>Short.</p>`},
		{name: "svg title", body: `<svg><title>404</title></svg><p>A short note.</p>`},
		{name: "long page with a 404 heading", body: `<html><h1>Page not found</h1>` + article + `</html>`},
		{name: "hidden login dialog", body: `<p>Welcome.</p><div style="display: none"><form><input type="password"></form></div>`},
		{name: "hidden 404 heading", body: `<title>Shop</title><p>Welcome.</p><div hidden><h1>Page not found</h1></div>`},
		{name: "markdown", body: "# Page not found\n\nGone.", contentType: TypeMarkdown},
		{name: "a pdf's text", body: "Password: <input type=password>", contentType: "application/pdf"},
	}
	for _, c := range cases {
		ct := c.contentType
		if ct == "" {
			ct = "text/html; charset=utf-8"
		}
		if got := LoginForm([]byte(c.body), ct); got != c.login {
			t.Errorf("%s: LoginForm = %v", c.name, got)
		}
		if got := NotFound([]byte(c.body), ct); got != c.notFound {
			t.Errorf("%s: NotFound = %v", c.name, got)
		}
	}
}
