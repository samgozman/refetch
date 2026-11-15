package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for refetch read-pdf.
func TestMain(m *testing.M) {
	if os.Getenv("REFETCH_TEST_PDF_CHILD") == "1" {
		os.Exit(ServePDF(context.Background(), os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// selfStreamPDF keeps its page tree in an object stream that is itself,
// which sends the parser into endless recursion.
func selfStreamPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	catalog := b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	xref := b.Len()
	entries := []byte{0, 0, 0, 0, 1, 0, byte(catalog), 0, 2, 0, 2, 0, 1, byte(xref >> 8), byte(xref), 0}
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /XRef /Size 4 /W [1 2 1] /Root 1 0 R /Length %d >>\nstream\n", len(entries))
	b.Write(entries)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestIsolatePDF(t *testing.T) {
	t.Setenv("REFETCH_TEST_PDF_CHILD", "1")
	old := readPDF
	t.Cleanup(func() { readPDF = old })
	IsolatePDF(os.Args[0])

	md, ok, err := Document(t.Context(), makePDF("BT /F1 12 Tf 72 720 Td (Quarterly report) Tj ET"))
	if !ok || err != nil || md != "Quarterly report" {
		t.Errorf("PDF: %v %v %q", ok, err, md)
	}
	_, _, err = Document(t.Context(), makePDF("0 0 1 rg 0 0 100 100 re f"))
	if !errors.Is(err, ErrDocument) || err.Error() != "could not read the document: the PDF has no text layer (a scan or images only)" {
		t.Errorf("scanned PDF: %v", err)
	}
	_, _, err = Document(t.Context(), selfStreamPDF())
	if !errors.Is(err, ErrDocument) || !strings.Contains(err.Error(), "the reader crashed") {
		t.Errorf("a PDF that crashes the parser: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = Document(ctx, makePDF("BT ET")); !errors.Is(err, ErrDocument) || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

func TestServePDF(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := ServePDF(t.Context(), bytes.NewReader(makePDF("BT /F1 12 Tf 72 720 Td (Hello) Tj ET")), &out, &errOut); code != 0 || out.String() != "Hello" {
		t.Errorf("ServePDF = %d, %q, %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := ServePDF(t.Context(), strings.NewReader("%PDF-1.7\n\x00 garbage"), &out, &errOut); code != 1 || out.Len() != 0 || errOut.Len() == 0 {
		t.Errorf("broken PDF: %d, %q, %q", code, out.String(), errOut.String())
	}
}
