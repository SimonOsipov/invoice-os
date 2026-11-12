package extraction_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// pcapText counts reads and answers err.
type pcapText struct {
	calls atomic.Int32
	err   error
}

func (r *pcapText) Name() string    { return "stub" }
func (r *pcapText) Version() string { return "1" }
func (r *pcapText) Read(context.Context, extraction.Document, func(extraction.Page) error) (extraction.PageResult, error) {
	r.calls.Add(1)
	return extraction.PageResult{}, r.err
}

func readPDFForCheck(text *pcapText, file []byte) error {
	r := extraction.CheckReaders{Text: text, Cap: extraction.NewPDFiumReader()}
	_, _, err := extraction.ReadForCheck(context.Background(), r, file, "pdf")
	return err
}

func TestReadForCheck_APDFOverThePageCapNeverReachesTheTextReader(t *testing.T) {
	text := &pcapText{}
	err := readPDFForCheck(text, fxBuildNPage(prOverCap))
	if !errors.Is(err, extraction.ErrTooManyPages) || !errors.Is(err, extraction.ErrDocumentUnreadable) {
		t.Fatalf("err = %v, want ErrTooManyPages and ErrDocumentUnreadable", err)
	}
	if n := text.calls.Load(); n != 0 {
		t.Errorf("text reader calls = %d, want 0", n)
	}
}

func TestReadForCheck_APDFAtThePageCapReachesTheTextReader(t *testing.T) {
	sentinel := errors.New("text reader reached")
	text := &pcapText{err: sentinel}
	err := readPDFForCheck(text, fxBuildNPage(prAtCap))
	if !errors.Is(err, sentinel) || errors.Is(err, extraction.ErrTooManyPages) {
		t.Fatalf("err = %v, want the text reader's sentinel and not ErrTooManyPages", err)
	}
	if n := text.calls.Load(); n != 1 {
		t.Errorf("text reader calls = %d, want 1", n)
	}
}

func TestReadForCheck_APDFPdfiumCannotOpenStillReachesTheTextReader(t *testing.T) {
	sentinel := errors.New("text reader reached")
	text := &pcapText{err: sentinel}
	if err := readPDFForCheck(text, []byte("%PDF-1.4 not a pdf")); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the text reader's sentinel", err)
	}
	if n := text.calls.Load(); n != 1 {
		t.Errorf("text reader calls = %d, want 1", n)
	}
}
