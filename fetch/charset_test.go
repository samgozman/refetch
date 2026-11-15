package fetch

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
)

func TestToUTF8(t *testing.T) {
	sjis, _ := japanese.ShiftJIS.NewEncoder().String("<p>日本語のページ</p>")
	cp1251, _ := charmap.Windows1251.NewEncoder().String(`<html><head><meta charset="windows-1251"></head><body>Привет</body></html>`)
	latin1, _ := charmap.ISO8859_1.NewEncoder().String("<p>café</p>")
	cases := map[string]struct {
		body, ct, want, wantCT string
	}{
		"header charset":       {sjis, "text/html; charset=Shift_JIS", "日本語のページ", "text/html; charset=utf-8"},
		"meta charset":         {cp1251, "text/html", "Привет", "text/html; charset=utf-8"},
		"utf-8 under a legacy": {`<meta charset="iso-8859-1"><p>café</p>`, "text/html", "café", "text/html"},
		"plain utf-8":          {"<p>naïve</p>", "text/html; charset=utf-8", "naïve", "text/html; charset=utf-8"},
		"guess when invalid":   {latin1, "text/html", "café", "text/html; charset=utf-8"},
		"no content type":      {latin1, "", "café", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body, ct := toUTF8([]byte(tc.body), tc.ct)
			if !strings.Contains(string(body), tc.want) || ct != tc.wantCT {
				t.Errorf("toUTF8 = %q, %q; want %q in it, %q", body, ct, tc.want, tc.wantCT)
			}
		})
	}
}

func TestGetDecodesCharset(t *testing.T) {
	const u = "https://example.jp/"
	sjis, _ := japanese.ShiftJIS.NewEncoder().String("<html><body><h1>お知らせ</h1><p>" + strings.Repeat("本文です。", 60) + "</p></body></html>")
	f := &Fetcher{chrome: fake(map[string]page{u: {status: 200, body: sjis, ct: "text/html; charset=shift_jis"}}, nil)}
	p, err := f.Get(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Markdown(p.Body, p.ContentType, nil), "# お知らせ") {
		t.Errorf("Shift_JIS page not decoded: %.200q", p.Body)
	}
}
