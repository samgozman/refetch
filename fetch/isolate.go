package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// readPDF reads a PDF's text: in this process, or in a child once
// IsolatePDF is called.
var readPDF = pdfWithin

// IsolatePDF reads every PDF in a child process, name run with args, that
// runs ServePDF. The parser recurses without end on some malformed files,
// a stack overflow that no recover catches; in a child it ends only the
// child, and a timeout kills the parse instead of leaving it to run.
func IsolatePDF(name string, args ...string) {
	readPDF = func(ctx context.Context, body []byte) (string, error) {
		child, cancel := context.WithTimeout(ctx, pdfTimeout)
		defer cancel()
		cmd := exec.CommandContext(child, name, args...) //nolint:gosec // refetch's own binary, set once at startup
		cmd.Stdin = bytes.NewReader(body)
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		ee, exited := errors.AsType[*exec.ExitError](err)
		switch {
		case err == nil:
			return string(out), nil
		case ctx.Err() != nil:
			return "", fmt.Errorf("%w: %w", ErrDocument, ctx.Err())
		case child.Err() != nil:
			return "", fmt.Errorf("%w: the PDF took over %s to read", ErrDocument, pdfTimeout)
		case exited && ee.ExitCode() == 1:
			return "", fmt.Errorf("%w: %s", ErrDocument, strings.TrimSpace(string(ee.Stderr)))
		case exited:
			return "", fmt.Errorf("%w: malformed PDF (the reader crashed)", ErrDocument)
		}
		return "", fmt.Errorf("%w: %w", ErrDocument, err)
	}
}

// ServePDF is the child side of IsolatePDF: it reads a PDF from in and
// writes its text to out, or why it could not to errOut and returns 1.
func ServePDF(ctx context.Context, in io.Reader, out, errOut io.Writer) int {
	body, err := io.ReadAll(io.LimitReader(in, MaxDocBytes+1))
	var md string
	if err == nil {
		md, err = pdfWithin(ctx, body)
	}
	if err == nil {
		_, err = io.WriteString(out, md)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errOut, strings.TrimPrefix(err.Error(), ErrDocument.Error()+": "))
		return 1
	}
	return 0
}
