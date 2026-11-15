package fetch

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

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
func Document(ctx context.Context, body []byte) (md string, ok bool, err error) {
	switch {
	case bytes.HasPrefix(body, pdfMagic):
		md, err = pdfWithin(ctx, body)
		return md, true, err
	case bytes.HasPrefix(body, zipMagic):
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

var pdfMagic, zipMagic = []byte("%PDF-"), []byte("PK\x03\x04")

// mayBeDocument reports whether body starts the way a PDF or office file does.
func mayBeDocument(body []byte) bool {
	return bytes.HasPrefix(body, pdfMagic) || bytes.HasPrefix(body, zipMagic)
}

// pdfTimeout caps a PDF's reading, for malformed files the walk limits do
// not foresee. Past it the call gives up, and the parse is left to end on
// its own.
var pdfTimeout = 20 * time.Second // tests shorten it

func pdfWithin(ctx context.Context, body []byte) (string, error) {
	type result struct {
		md  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		md, err := pdfText(body)
		done <- result{md, err}
	}()
	timer := time.NewTimer(pdfTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.md, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %w", ErrDocument, ctx.Err())
	case <-timer.C:
		return "", fmt.Errorf("%w: the PDF took over %s to read", ErrDocument, pdfTimeout)
	}
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
	for i, p := range pdfPages(r) {
		if b.Len() >= MaxDocBytes {
			break
		}
		if !shallow(p.V) {
			return "", fmt.Errorf("%w: page %d sits deeper than %d levels in the page tree", ErrDocument, i+1, maxTreeDepth)
		}
		n, err := pageCost(p, min(budget, maxPageUnpacked))
		if err != nil {
			return "", fmt.Errorf("%w: page %d %w", ErrDocument, i+1, err)
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

// pdfPages lists a PDF's pages in order, walking the page tree once. The
// parser's own lookup walks the tree again for every page and loops
// forever on a tree that holds itself; this walk stops at maxTreeDepth
// levels and maxTreeNodes nodes.
func pdfPages(r *pdf.Reader) []pdf.Page {
	var out []pdf.Page
	nodes := 0
	var walk func(v pdf.Value, depth int)
	walk = func(v pdf.Value, depth int) {
		kids := v.Key("Kids")
		for i := 0; i < kids.Len() && len(out) < maxPDFPages && nodes < maxTreeNodes; i++ {
			nodes++
			switch kid := kids.Index(i); kid.Key("Type").Name() {
			case "Pages":
				if depth < maxTreeDepth {
					walk(kid, depth+1)
				}
			case "Page":
				out = append(out, pdf.Page{V: kid})
			}
		}
	}
	if root := r.Trailer().Key("Root").Key("Pages"); root.Key("Type").Name() == "Pages" {
		walk(root, 1)
	}
	return out
}

// shallow reports whether a page's chain of parents ends within
// maxTreeDepth levels; the parser follows it unbounded to find inherited
// resources.
func shallow(v pdf.Value) bool {
	for range maxTreeDepth + 1 {
		if v.IsNull() {
			return true
		}
		v = v.Key("Parent")
	}
	return false
}

// Limits on a PDF. The parser has no cap on what a compressed stream
// unpacks to, holds every glyph of a page as a text run, and parses a
// font's character map again at every font switch.
const (
	maxPDFPages     = 2000
	maxTreeDepth    = 64
	maxTreeNodes    = 4 * maxPDFPages
	maxPageUnpacked = 8 << 20
	maxPDFUnpacked  = 64 << 20
	maxPageGlyphs   = 200000
	maxFontSwitches = 20000
)

// errTooBig ends a page's measurement early.
var errTooBig = errors.New("asks for more work than refetch does")

// pageCost measures, before the parser reads a page, the bytes its content
// and font streams unpack to (at most limit), its glyphs and the character
// map parsing its font switches cost. It returns the bytes unpacked.
func pageCost(p pdf.Page, limit int64) (n int64, err error) {
	contents := p.V.Key("Contents")
	n = streams(contents, limit)
	var cmap int64
	for _, name := range p.Fonts() {
		f := p.Font(name)
		size := streams(f.V.Key("ToUnicode"), limit-n) + streams(f.V.Key("Encoding"), limit-n)
		n, cmap = n+size, max(cmap, size)
	}
	if n > limit {
		return 0, fmt.Errorf("unpacks to more than refetch reads (%d MB a page, %d MB a file)", maxPageUnpacked>>20, maxPDFUnpacked>>20)
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && errors.Is(e, errTooBig) {
				err = errTooBig
				return
			}
			panic(r)
		}
	}()
	glyphs, switches := 0, int64(0)
	pdf.Interpret(contents, func(stk *pdf.Stack, op string) {
		var args []pdf.Value
		for stk.Len() > 0 {
			args = append(args, stk.Pop())
		}
		switch op {
		case "Tf":
			switches++
		case "Tj", "'", `"`, "TJ":
			glyphs += showLen(args)
		}
		if glyphs > maxPageGlyphs || switches > maxFontSwitches || switches*cmap > maxPDFUnpacked {
			panic(errTooBig)
		}
	})
	return n, nil
}

// showLen counts the bytes a text-showing operator draws: its string, or
// the strings of its array.
func showLen(args []pdf.Value) int {
	total := 0
	for _, a := range args {
		switch a.Kind() {
		case pdf.String:
			total += len(a.RawString())
		case pdf.Array:
			for i := range a.Len() {
				if e := a.Index(i); e.Kind() == pdf.String {
					total += len(e.RawString())
				}
			}
		default:
		}
	}
	return total
}

// streams reads a stream, or an array of them (one level: an array can
// hold itself), to its end and returns the bytes it unpacks to, counted up
// to limit and past it by one.
func streams(v pdf.Value, limit int64) int64 {
	if v.Kind() == pdf.Array {
		var n int64
		for i := 0; i < v.Len() && n <= limit; i++ {
			n += stream(v.Index(i), limit-n)
		}
		return n
	}
	return stream(v, limit)
}

func stream(v pdf.Value, limit int64) int64 {
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
