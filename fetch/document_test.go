package fetch

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// makePDF builds a one-page PDF whose page draws content with Helvetica.
func makePDF(content string) []byte { return makePDFStream(content, "") }

// makePDFStream is makePDF with the content stored under filter.
func makePDFStream(content, filter string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d %s >>\nstream\n%s\nendstream", len(content), filter, content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// makeZip builds an office file from its parts.
func makeZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const (
	wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	rNS = `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	aNS = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`
	pNS = `xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`
)

func wPara(style, text string) string {
	props := ""
	if style != "" {
		props = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + props + `<w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func TestDocumentPDF(t *testing.T) {
	md, ok, err := Document(t.Context(), makePDF("BT /F1 12 Tf 72 720 Td (Quarterly report) Tj 0 -14 Td (second line) Tj 0 -40 Td (A new paragraph) Tj ET"))
	if !ok || err != nil || md != "Quarterly report\nsecond line\n\nA new paragraph" {
		t.Errorf("PDF: %v %v %q", ok, err, md)
	}
	_, ok, err = Document(t.Context(), makePDF("0 0 1 rg 0 0 100 100 re f"))
	if !ok || !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "no text layer") {
		t.Errorf("scanned PDF: %v %v", ok, err)
	}
	if _, ok, err = Document(t.Context(), []byte("%PDF-1.7\n\x00 garbage")); !ok || !errors.Is(err, ErrDocument) {
		t.Errorf("broken PDF: %v %v", ok, err)
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write([]byte("BT /F1 12 Tf 72 720 Td (x) Tj ET " + strings.Repeat(" ", maxPageUnpacked)))
	_ = zw.Close()
	if _, ok, err = Document(t.Context(), makePDFStream(z.String(), "/Filter /FlateDecode")); !ok || !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "unpacks to more") {
		t.Errorf("a stream that unpacks past the limit: %v %v", ok, err)
	}
	z.Reset()
	zw = zlib.NewWriter(&z)
	_, _ = zw.Write([]byte("BT /F1 12 Tf 72 720 Td (Compressed text) Tj ET"))
	_ = zw.Close()
	if md, ok, err := Document(t.Context(), makePDFStream(z.String(), "/Filter /FlateDecode")); !ok || err != nil || md != "Compressed text" {
		t.Errorf("compressed PDF: %v %v %q", ok, err, md)
	}
}

func TestDocumentPDFWorkLimits(t *testing.T) {
	glyphs := "BT /F1 12 Tf 72 720 Td (" + strings.Repeat("a", maxPageGlyphs+1) + ") Tj ET"
	switches := "BT " + strings.Repeat("/F1 12 Tf ", maxFontSwitches+1) + "72 720 Td (x) Tj ET"
	for name, content := range map[string]string{"glyphs": glyphs, "font switches": switches} {
		if _, ok, err := Document(t.Context(), makePDF(content)); !ok || !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "more work") {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}

	old := pdfTimeout
	pdfTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pdfTimeout = old })
	loop := bytes.Replace(makePDF("BT ET"), []byte("/Kids [3 0 R] /Count 1"), []byte("/Kids [2 0 R] /Count 9"), 1)
	if _, ok, err := Document(t.Context(), loop); !ok || !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "took over") {
		t.Errorf("a page tree that holds itself: %v %v", ok, err)
	}
}

func TestDocumentDocx(t *testing.T) {
	body := wPara("Title", "Field guide") + wPara("Heading2", "Birds") +
		wPara("", "Common birds of the <w:r><w:t>valley</w:t></w:r>") +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>wren</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc>` + wPara("", "Name") + `</w:tc><w:tc>` + wPara("", "Wingspan") + `</w:tc></w:tr>` +
		`<w:tr><w:tc>` + wPara("", "Robin") + `</w:tc><w:tc>` + wPara("", "20 | 22 cm") + `</w:tc></w:tr></w:tbl>` +
		wPara("", "Line one<w:br/>line two")
	doc := makeZip(t, map[string]string{"word/document.xml": `<w:document ` + wNS + `><w:body>` + body + `</w:body></w:document>`})
	md, ok, err := Document(t.Context(), doc)
	want := "# Field guide\n\n## Birds\n\nCommon birds of the valley\n\n- wren\n\n| Name | Wingspan |\n| --- | --- |\n| Robin | 20 \\| 22 cm |\n\nLine one\nline two"
	if !ok || err != nil || md != want {
		t.Errorf("docx: %v %v\n%s", ok, err, md)
	}
}

func TestDocumentXlsx(t *testing.T) {
	book := makeZip(t, map[string]string{
		"xl/workbook.xml": `<workbook ` + rNS + `><sheets><sheet name="Prices" r:id="rId1"/><sheet name="Empty" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst><si><t>Item</t></si><si><r><t>Pri</t></r><r><t>ce</t></r><rPh><t>x</t></rPh></si><si><t>Tea</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>` +
			`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="b"><v>1</v></c><c r="C2"><v>4.5</v></c></row>` +
			`<row r="3"><c r="A3" t="inlineStr"><is><t>Cake</t></is></c><c r="C3"><v>3</v></c></row>` +
			`</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet><sheetData/></worksheet>`,
	})
	md, ok, err := Document(t.Context(), book)
	want := "## Prices\n\n| Item |  | Price |\n| --- | --- | --- |\n| Tea | TRUE | 4.5 |\n| Cake |  | 3 |"
	if !ok || err != nil || md != want {
		t.Errorf("xlsx: %v %v\n%s", ok, err, md)
	}
}

func TestDocumentXlsxLimits(t *testing.T) {
	var rows strings.Builder
	for r := 1; r <= 5000; r++ {
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d" t="s"><v>0</v></c><c r="B%d" t="s"><v>0</v></c></row>`, r, r, r)
	}
	book := makeZip(t, map[string]string{
		"xl/workbook.xml":            `<workbook ` + rNS + `><sheets><sheet name="Big" r:id="rId1"/><sheet name="Never" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>` + strings.Repeat("long text ", maxSheetText/2000) + `</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData>` + rows.String() + `</sheetData></worksheet>`,
	})
	md, ok, err := Document(t.Context(), book)
	if !ok || err != nil || len(md) > maxSheetText+64<<10 || !strings.Contains(md, "(stopped at") || strings.Contains(md, "Never") {
		t.Errorf("xlsx limits: %v %v, %d bytes, ends %q", ok, err, len(md), md[max(0, len(md)-80):])
	}
	if c := cellText(strings.Repeat("é", 5000)); len([]rune(c)) != maxCellRunes+1 {
		t.Errorf("cellText kept %d runes", len([]rune(c)))
	}
}

func TestDocumentPptx(t *testing.T) {
	slide := func(texts ...string) string {
		var b strings.Builder
		for _, s := range texts {
			b.WriteString(`<a:p><a:r><a:t>` + s + `</a:t></a:r></a:p>`)
		}
		return `<p:sld ` + aNS + ` ` + pNS + `><p:cSld><p:spTree><p:sp><p:txBody>` + b.String() + `</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	deck := makeZip(t, map[string]string{
		"ppt/presentation.xml": `<p:presentation ` + pNS + ` ` + rNS + `><p:sldIdLst><p:sldId id="257" r:id="rId3"/><p:sldId id="256" r:id="rId2"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId2" Target="slides/slide1.xml"/>` +
			`<Relationship Id="rId3" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml": slide("Roadmap", "Ship in May"),
		"ppt/slides/slide2.xml": slide("Welcome"),
	})
	md, ok, err := Document(t.Context(), deck)
	if want := "## Slide 1\n\nWelcome\n\n## Slide 2\n\nRoadmap\n\nShip in May"; !ok || err != nil || md != want {
		t.Errorf("pptx: %v %v\n%s", ok, err, md)
	}
}

func TestDocumentOther(t *testing.T) {
	for name, body := range map[string][]byte{
		"png":       []byte("\x89PNG\r\n\x1a\n"),
		"plain zip": makeZip(t, map[string]string{"readme.txt": "hi"}),
		"bad zip":   []byte("PK\x03\x04 not really"),
	} {
		if _, ok, err := Document(t.Context(), body); ok || err != nil {
			t.Errorf("%s: ok %v, err %v", name, ok, err)
		}
	}
	bomb := makeZip(t, map[string]string{"word/document.xml": `<w:document ` + wNS + `><w:body>` + strings.Repeat(" ", maxPartBytes+1) + `</w:body></w:document>`})
	if _, ok, err := Document(t.Context(), bomb); !ok || !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "unpacks to more than") {
		t.Errorf("oversized part: %v %v", ok, err)
	}
	if _, ok, err := Document(t.Context(), makeZip(t, map[string]string{"word/document.xml": "<w:document><unclosed"})); !ok || !errors.Is(err, ErrDocument) {
		t.Errorf("broken XML: %v %v", ok, err)
	}
}

func TestGetReadsADocument(t *testing.T) {
	const u = "https://example.com/report.pdf"
	pdf := string(makePDF("BT /F1 12 Tf 72 720 Td (Quarterly report) Tj ET"))
	f := &Fetcher{chrome: fake(map[string]page{u: {status: 200, body: pdf, ct: "application/octet-stream"}}, nil)}
	p, err := f.Get(t.Context(), u)
	if err != nil || string(p.Body) != "Quarterly report" || p.ContentType != TypeMarkdown {
		t.Errorf("Get: %v, %q %q", err, p.Body, p.ContentType)
	}
}

func TestReadBody(t *testing.T) {
	big := strings.Repeat("x", MaxBytes+10)
	if b, err := readBody(strings.NewReader(big), "text/html"); err != nil || len(b) != MaxBytes {
		t.Errorf("a page is cut at MaxBytes: %d, %v", len(b), err)
	}
	if b, err := readBody(strings.NewReader(big), "application/pdf"); err != nil || len(b) != len(big) {
		t.Errorf("a document may pass MaxBytes: %d, %v", len(b), err)
	}
	if _, err := readBody(strings.NewReader(strings.Repeat("x", MaxDocBytes+1)), "application/octet-stream"); err == nil {
		t.Error("a document over MaxDocBytes must fail")
	}
}

func TestColumn(t *testing.T) {
	for ref, want := range map[string]int{"A1": 0, "C7": 2, "Z9": 25, "AB12": 27, "": -1, "12": -1} {
		if got := column(ref); got != want {
			t.Errorf("column(%q) = %d, want %d", ref, got, want)
		}
	}
}
