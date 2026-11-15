package fetch

import "testing"

func TestRewrite(t *testing.T) {
	cases := map[string]string{
		"https://docs.google.com/document/d/1AbC_d-9/edit?usp=sharing":       "https://docs.google.com/document/d/1AbC_d-9/export?format=html",
		"http://docs.google.com/presentation/d/XyZ/edit#slide=id.p":          "https://docs.google.com/presentation/d/XyZ/export?format=pptx",
		"https://docs.google.com/spreadsheets/d/S1/edit#gid=42":              "https://docs.google.com/spreadsheets/d/S1/gviz/tq?tqx=out:html&gid=42",
		"https://docs.google.com/spreadsheets/d/S1/edit?gid=7":               "https://docs.google.com/spreadsheets/d/S1/gviz/tq?tqx=out:html&gid=7",
		"https://docs.google.com/spreadsheets/d/S1":                          "https://docs.google.com/spreadsheets/d/S1/gviz/tq?tqx=out:html",
		"https://drive.google.com/file/d/F1/view?usp=drive_link":             "https://drive.google.com/uc?export=download&id=F1",
		"https://drive.google.com/open?id=F2":                                "https://drive.google.com/uc?export=download&id=F2",
		"https://docs.google.com/document/d/e/2PACX-1v/pub":                  "",
		"https://docs.google.com/forms/d/F3/viewform":                        "",
		"https://docs.google.com/document/u/0/":                              "",
		"https://drive.google.com/drive/folders/D1":                          "",
		"https://example.com/document/d/1AbC/edit":                           "",
		"ftp://docs.google.com/document/d/1AbC/edit":                         "",
		"https://docs.google.com/spreadsheets/d/S1/edit#gid=1&range=A1%3AB2": "https://docs.google.com/spreadsheets/d/S1/gviz/tq?tqx=out:html&gid=1",
	}
	for in, want := range cases {
		if got := Rewrite(in); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", in, got, want)
		}
	}
}
