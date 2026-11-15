package fetch

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ErrDocument is a PDF or office file that could not be turned into text.
var ErrDocument = errors.New("could not read the document")

// MaxDocBytes caps a PDF or office file, which runs larger than a page.
const MaxDocBytes = 32 << 20

const (
	typePDF  = "application/pdf"
	typeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	typeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	typePptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// docTypes are a document's own media types and the generic ones file hosts send.
var docTypes = []string{typePDF, typeDocx, typeXlsx, typePptx, "application/octet-stream", "application/zip", "application/x-pdf", "binary/octet-stream"}

// isDocType reports whether a response of media type mt may be a document,
// and so may use MaxDocBytes.
func isDocType(mt string) bool { return slices.Contains(docTypes, mt) }

// Document returns a PDF, Word, Excel or PowerPoint file as Markdown, found
// by its bytes, not its name or type. ok is false for any other file; err
// is set for a document that could not be read, wrapping ErrDocument.
func Document(body []byte) (md string, ok bool, err error) {
	switch {
	case bytes.HasPrefix(body, []byte("%PDF-")):
		md, err = pdfText(body)
		return md, true, err
	case bytes.HasPrefix(body, []byte("PK\x03\x04")):
		zr, zerr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if zerr != nil {
			return "", false, nil //nolint:nilerr // not a zip after all: a file of no type refetch reads
		}
		o := &office{zr: zr}
		switch {
		case o.has("word/document.xml"):
			md, err = o.docx()
		case o.has("xl/workbook.xml"):
			md, err = o.xlsx()
		case o.has("ppt/presentation.xml"):
			md, err = o.pptx()
		default:
			return "", false, nil
		}
		return md, true, err
	}
	return "", false, nil
}

// pdfText reads the text layer of every page; a scanned PDF has none.
func pdfText(body []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil { // the parser panics on some malformed files
			text, err = "", fmt.Errorf("%w: malformed PDF", ErrDocument)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDocument, err)
	}
	var b strings.Builder
	budget := int64(maxPDFUnpacked)
	for i := 1; i <= min(r.NumPage(), maxPDFPages) && b.Len() < MaxDocBytes; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		limit := min(budget, maxPageUnpacked)
		n := pageUnpacked(p, limit)
		if n > limit {
			return "", fmt.Errorf("%w: page %d unpacks to more than refetch reads (%d MB a page, %d MB a file)",
				ErrDocument, i, maxPageUnpacked>>20, maxPDFUnpacked>>20)
		}
		budget -= n
		if page := pageLines(p.Content().Text); page != "" {
			b.WriteString(page)
			b.WriteString("\n\n")
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", fmt.Errorf("%w: the PDF has no text layer (a scan or images only)", ErrDocument)
	}
	return strings.TrimSpace(b.String()), nil
}

// Limits on a PDF: the parser holds a page's text runs in memory and has no
// cap of its own on what a compressed stream unpacks to.
const (
	maxPDFPages     = 2000
	maxPageUnpacked = 8 << 20
	maxPDFUnpacked  = 64 << 20
)

// pageUnpacked is what the streams the parser reads for a page (its content
// and its fonts' character maps) unpack to, counted up to limit and past
// it by one: more than limit means the page is over.
func pageUnpacked(p pdf.Page, limit int64) int64 {
	vals := []pdf.Value{p.V.Key("Contents")}
	for _, name := range p.Fonts() {
		f := p.Font(name)
		vals = append(vals, f.V.Key("ToUnicode"), f.V.Key("Encoding"))
	}
	var n int64
	for _, v := range vals {
		if v.Kind() == pdf.Array {
			for i := range v.Len() {
				n += streamLen(v.Index(i), limit-n)
			}
		} else {
			n += streamLen(v, limit-n)
		}
		if n > limit {
			return limit + 1
		}
	}
	return n
}

// streamLen reads a stream value to its end, up to limit+1 bytes.
func streamLen(v pdf.Value, limit int64) int64 {
	if v.Kind() != pdf.Stream || limit < 0 {
		return 0
	}
	rc := v.Reader()
	defer func() { _ = rc.Close() }()
	n, _ := io.Copy(io.Discard, io.LimitReader(rc, limit+1))
	return n
}

// pageLines joins a page's text runs, in the order the page draws them, into
// lines: a run on another baseline starts a line, and a line gap wider than
// one and a half lines is a paragraph break. Where the parser knows a run's
// width, a run left of where the last one ended starts a line and a gap
// wider than a fifth of the font size is a space; with fonts it cannot
// measure, widths are 0 and only the text's own spaces count. The parser's
// "\n" runs mark text-positioning operators, mid-word as often as not.
func pageLines(runs []pdf.Text) string {
	var b strings.Builder
	var endX, y, size float64
	measured := false
	for _, t := range runs {
		if t.S == "" || t.S == "\n" {
			continue
		}
		fs := max(t.FontSize, 1)
		switch {
		case b.Len() == 0:
		case math.Abs(t.Y-y) > fs/2 || (measured && t.X < endX-fs):
			b.WriteByte('\n')
			if math.Abs(t.Y-y) > 1.6*max(size, fs) {
				b.WriteByte('\n')
			}
		case measured && t.X-endX > fs/5:
			b.WriteByte(' ')
		}
		b.WriteString(t.S)
		endX, y, size, measured = t.X+t.W, t.Y, fs, t.W > 0
	}
	return tidy(b.String())
}

// tidy collapses spaces, trims each line and keeps at most one empty line
// in a row.
func tidy(s string) string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		l = strings.Join(strings.Fields(strings.ReplaceAll(l, "\uFFFD", "")), " ") // U+FFFD: a glyph the font does not map
		if l == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
