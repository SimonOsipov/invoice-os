// publiccheck.go: the tenantless Readiness check's document read.
package extraction

import (
	"context"
	"errors"
	"slices"

	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
)

// CheckReaders are the readers a check reads one document with.
type CheckReaders struct {
	Text   PageReader // docling
	Render PageReader // pdfium, for a PDF with no text layer
	AI     AIReader
	Jev    JevAsker
}

var ErrCheckAIUnavailable = errors.New("extraction: AI reading unavailable for a check")

// ReadForCheck reads one PDF or DOCX as worker.go's text and no-text arms do, storing nothing.
// ceiling: the two sequences can drift; extract one helper when the worker's text arm next changes.
func ReadForCheck(ctx context.Context, r CheckReaders, file []byte, format string) (fields []FieldResult, docType string, err error) {
	if r.Text == nil {
		return nil, "", errors.New("extraction: check has no text reader")
	}
	doc := Document{Bytes: file, ContentType: acceptedDocumentTypes["."+format]}
	textPages, tokens, res, err := readText(ctx, r.Text, doc)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	actx := airecord.WithPublicCheck(ctx)
	aiOn := r.AI != nil && r.AI.Enabled()

	if res.TextChars == 0 {
		fields = poorScanResults()
		if format == "pdf" && aiOn && r.Render != nil {
			pngs, err := renderEnds(ctx, r.Render, doc)
			if err != nil {
				return nil, "", err
			}
			if len(pngs) > 0 {
				answer, failed := askAIPages(actx, r.AI, pngs, string(file))
				if err := ctx.Err(); err != nil {
					return nil, "", err
				}
				if failed {
					return nil, "", ErrCheckAIUnavailable
				}
				if len(answer) > 0 {
					fields = imageReading(answer)
				}
			}
		}
		return fields, "", nil
	}

	answer, failed := askAI(actx, r.AI, tokens)
	var lineAnswer []AILine
	if !failed {
		lineAnswer, failed = askAILines(actx, r.AI, tokens)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if failed {
		return nil, "", ErrCheckAIUnavailable
	}
	lines := LineItems(textPages)
	merged := mergeAI(Reconcile(Input{
		Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}),
		Lines:      lines,
		Entity:     Entity{},
		Pages:      tokens,
	}), answer, tokens, lines)
	fields = mergeAILines(merged, lineAnswer, tokens)
	fields, docType = checkDocument(actx, r.Jev, tokens, fields)
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return fields, docType, nil
}

func poorScanResults() []FieldResult {
	return []FieldResult{{
		Field:        Field{Name: doclingTextLayerField, Reason: ReasonUnreadable},
		Alternatives: []Field{},
	}}
}

// renderEnds renders the document and returns copies of the first and last page PNGs.
// ImagePNG is borrowed for the length of onPage, so each is copied.
func renderEnds(ctx context.Context, r PageReader, doc Document) ([][]byte, error) {
	var first, last []byte
	n := 0
	_, err := r.Read(ctx, doc, func(p Page) error {
		if n == 0 {
			first = slices.Clone(p.ImagePNG)
		}
		last = append(last[:0], p.ImagePNG...)
		n++
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch n {
	case 0:
		return nil, nil
	case 1:
		return [][]byte{first}, nil
	}
	return [][]byte{first, last}, nil
}
