package fetch

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// Limits on what an office file may unpack to, whatever its zip headers
// claim: per part, and for the whole file.
const (
	maxPartBytes  = 32 << 20
	maxOfficeRead = 96 << 20
	maxSheetCells = 100000
)

// office is a Word, Excel or PowerPoint file: a zip of XML parts.
type office struct {
	zr   *zip.Reader
	read int64 // bytes unpacked so far
}

func (o *office) has(name string) bool {
	_, err := o.zr.Open(name)
	return err == nil
}

// part unpacks one part, within the limits.
func (o *office) part(name string) ([]byte, error) {
	f, err := o.zr.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is missing", ErrDocument, name)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxPartBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDocument, err)
	}
	if o.read += int64(len(b)); len(b) > maxPartBytes || o.read > maxOfficeRead {
		return nil, fmt.Errorf("%w: the file unpacks to more than %d MB", ErrDocument, maxOfficeRead>>20)
	}
	return b, nil
}

// walk calls fn for every token of a part; names are local, without their
// namespace prefix.
func (o *office) walk(name string, fn func(xml.Token)) error {
	b, err := o.part(name)
	if err != nil {
		return err
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrDocument, name, err)
		}
		fn(tok)
	}
}

// rels maps a part's relationship ids to the parts they point at.
func (o *office) rels(part string) map[string]string {
	dir, file := path.Split(part)
	out := map[string]string{}
	_ = o.walk(dir+"_rels/"+file+".rels", func(tok xml.Token) {
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			target, absolute := strings.CutPrefix(attrOf(se, "Target"), "/")
			if !absolute {
				target = path.Join(dir, target)
			}
			out[attrOf(se, "Id")] = target
		}
	})
	return out
}

// ordered lists the parts a part's elements named elem point at through
// their r:id, in document order: the sheets of a workbook, the slides of a
// deck.
func (o *office) ordered(part, elem string) (names, targets []string, err error) {
	rels := o.rels(part)
	err = o.walk(part, func(tok xml.Token) {
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == elem {
			if t, ok := rels[relID(se)]; ok {
				names, targets = append(names, attrOf(se, "name")), append(targets, t)
			}
		}
	})
	return names, targets, err
}

// relID is an element's r:id, which a slide's sldId has besides its own id.
func relID(se xml.StartElement) string {
	for _, a := range se.Attr {
		if a.Name.Local == "id" && strings.HasSuffix(a.Name.Space, "/relationships") {
			return a.Value
		}
	}
	return ""
}

func attrOf(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// docx reads a Word document's body: headings, list items, paragraphs and
// tables.
func (o *office) docx() (string, error) {
	w := &wordDoc{}
	err := o.walk("word/document.xml", func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			w.start(t)
		case xml.CharData:
			if w.inText {
				w.para.Write(t)
			}
		case xml.EndElement:
			w.end(t.Name.Local)
		}
	})
	return joinBlocks(w.out), err
}

// wordDoc is the state of a docx walk. Only the outermost table is laid
// out; a nested one's paragraphs join its cell.
type wordDoc struct {
	out    []string
	para   strings.Builder
	prefix string // heading or list marker of the paragraph
	rows   [][]string
	row    []string
	cell   []string
	tables int // tables open
	inText bool
}

func (w *wordDoc) start(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		w.para.Reset()
		w.prefix = ""
	case "pStyle":
		w.prefix = headingPrefix(attrOf(t, "val"))
	case "numPr":
		w.prefix = cmp.Or(w.prefix, "- ")
	case "t":
		w.inText = true
	case "tab":
		w.para.WriteByte(' ')
	case "br", "cr":
		w.para.WriteByte('\n')
	case "tbl":
		w.tables++
		if w.tables == 1 {
			w.rows = nil
		}
	case "tr":
		if w.tables == 1 {
			w.row = nil
		}
	case "tc":
		if w.tables == 1 {
			w.cell = nil
		}
	}
}

func (w *wordDoc) end(name string) {
	switch name {
	case "t":
		w.inText = false
	case "p":
		text := strings.TrimSpace(w.para.String())
		switch {
		case text == "":
		case w.tables > 0:
			w.cell = append(w.cell, text)
		default:
			w.out = append(w.out, w.prefix+text)
		}
	case "tc":
		if w.tables == 1 {
			w.row = append(w.row, cellText(strings.Join(w.cell, " ")))
		}
	case "tr":
		if w.tables == 1 {
			w.rows = append(w.rows, w.row)
		}
	case "tbl":
		w.tables--
		if w.tables == 0 {
			w.out = append(w.out, gridTable(w.rows))
		}
	}
}

// headingPrefix is the Markdown heading for a Word paragraph style
// (Title, Heading1 to Heading6), or "".
func headingPrefix(style string) string {
	if style == "Title" {
		return "# "
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(style, "Heading")); err == nil && n >= 1 && n <= 6 {
		return strings.Repeat("#", n) + " "
	}
	return ""
}

// xlsx reads every sheet of a workbook as a table under its name.
func (o *office) xlsx() (string, error) {
	var shared []string
	var si strings.Builder
	inT, phonetic := false, false
	_ = o.walk("xl/sharedStrings.xml", func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				si.Reset()
			case "t":
				inT = true
			case "rPh": // a reading guide above the text, not the text
				phonetic = true
			}
		case xml.CharData:
			if inT && !phonetic {
				si.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "rPh":
				phonetic = false
			case "si":
				shared = append(shared, si.String())
			}
		}
	})
	names, sheets, err := o.ordered("xl/workbook.xml", "sheet")
	if err != nil {
		return "", err
	}
	var out []string
	cells := 0
	for i, sheet := range sheets {
		rows, err := o.sheet(sheet, shared, &cells)
		if err != nil {
			return "", err
		}
		if len(rows) > 0 {
			out = append(out, "## "+cellText(names[i]), gridTable(rows))
		}
		if cells > maxSheetCells {
			out = append(out, fmt.Sprintf("(stopped after %d cells)", maxSheetCells))
			break
		}
	}
	return joinBlocks(out), nil
}

// sheet reads one worksheet's cells into rows, placing each by its
// reference (B3) so that empty cells keep their column.
func (o *office) sheet(name string, shared []string, cells *int) ([][]string, error) {
	var rows [][]string
	var row []string
	var kind, ref string
	var val strings.Builder
	inV := false
	err := o.walk(name, func(tok xml.Token) {
		if *cells > maxSheetCells {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				kind, ref = attrOf(t, "t"), attrOf(t, "r")
				val.Reset()
			case "v", "t":
				inV = true
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inV = false
			case "c":
				row = placeCell(row, column(ref), cellValue(kind, val.String(), shared))
				*cells++
			case "row":
				rows = append(rows, row)
			}
		}
	})
	return rows, err
}

// cellValue is a cell's text: a shared string by its index, a boolean as
// TRUE or FALSE, anything else as stored.
func cellValue(kind, v string, shared []string) string {
	switch kind {
	case "s":
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(shared) {
			return shared[n]
		}
	case "b":
		return map[string]string{"1": "TRUE", "0": "FALSE"}[v]
	}
	return v
}

// placeCell puts v in column col of row, after empty cells for any skipped
// columns, up to maxTableWidth columns.
func placeCell(row []string, col int, v string) []string {
	if col >= len(row) && col < maxTableWidth {
		row = append(row, make([]string, col-len(row))...)
	}
	if len(row) < maxTableWidth {
		row = append(row, cellText(v))
	}
	return row
}

// column is the zero-based column of a cell reference (A1 is 0, AB7 is
// 27), or -1.
func column(ref string) int {
	n := 0
	for i := 0; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A') + 1
	}
	return n - 1
}

// pptx reads a deck's slides in order, each under its number.
func (o *office) pptx() (string, error) {
	_, slides, err := o.ordered("ppt/presentation.xml", "sldId")
	if err != nil {
		return "", err
	}
	var out []string
	for i, slide := range slides {
		var lines []string
		var para strings.Builder
		inT := false
		err := o.walk(slide, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "p":
					para.Reset()
				case "t":
					inT = true
				case "br":
					para.WriteByte(' ')
				}
			case xml.CharData:
				if inT {
					para.Write(t)
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inT = false
				case "p":
					if s := strings.TrimSpace(para.String()); s != "" {
						lines = append(lines, s)
					}
				}
			}
		})
		if err != nil {
			return "", err
		}
		out = append(out, fmt.Sprintf("## Slide %d", i+1))
		if len(lines) > 0 {
			out = append(out, strings.Join(lines, "\n\n"))
		}
	}
	return joinBlocks(out), nil
}

// cellText makes a value fit in one table cell.
func cellText(s string) string {
	return strings.ReplaceAll(oneLineText(s), "|", `\|`)
}

// gridTable lays out rows as a Markdown table, the first row as its header;
// a single cell or column is plain lines.
func gridTable(rows [][]string) string {
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	rows, width = trimTable(rows, width)
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	if width < 2 || len(rows) < 2 {
		for _, r := range rows {
			b.WriteString(strings.Join(r, " ") + "\n")
		}
		return strings.TrimSpace(b.String())
	}
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			b.WriteString(strings.Repeat("| --- ", width) + "|\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func joinBlocks(blocks []string) string {
	var out []string
	for _, b := range blocks {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n\n")
}
