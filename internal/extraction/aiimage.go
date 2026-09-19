// aiimage.go: the image request, page choice and format-only reading for a document with no
// text layer.
package extraction

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// aiImageIntro is tools/aimodeltest/run.py's IMAGE_INTRO, byte for byte.
const aiImageIntro = "The document has no text layer. Its pages are attached as images."

// aiImagePages keeps the first and last page: header fields sit there; middle pages carry line
// items (AIR-08). Never mutates images.
func aiImagePages(images []PageImage) []PageImage {
	switch len(images) {
	case 0:
		return nil
	case 1:
		return []PageImage{images[0]}
	default:
		return []PageImage{images[0], images[len(images)-1]}
	}
}

// readPagePNGs reads each page back by its storage key and closes every body.
func readPagePNGs(ctx context.Context, read PageObject, pages []PageImage) ([][]byte, error) {
	out := make([][]byte, 0, len(pages))
	for _, p := range pages {
		body, _, err := read(ctx, p.StorageKey)
		if err != nil {
			return nil, err
		}
		b, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		out = append(out, b)
	}
	return out, nil
}

// askAIPages asks once with page images. hint steers the fake only; the client never sends or
// logs it.
func askAIPages(ctx context.Context, r AIReader, pngs [][]byte, hint string) (map[string]string, bool) {
	return callAI(ctx, r, ai.Request{
		Purpose:    ai.PurposeDocument,
		System:     aiSystem,
		Text:       aiImageIntro,
		Pages:      pngs,
		FakeHint:   hint,
		SchemaName: "invoice_fields",
		Schema:     aiFieldSchema,
	})
}

// imageReading decides each header field from the answer alone: format check only (AIR-03 check
// (a), no exception (d)), no region -- checks (b)-(d) all read text in front of the value, and
// there is none.
func imageReading(answer map[string]string) []FieldResult {
	out := Reconcile(Input{})
	for i := range out {
		name := out[i].Name
		if !slices.Contains(HeaderFields, name) {
			continue
		}
		raw, ok := answer[name]
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		if _, want, ok := aiReadings(name, raw); ok && len(want) == 1 {
			value := want[0]
			out[i] = FieldResult{
				Field:        Field{Name: name, Value: &value, Reason: ReasonNone},
				Alternatives: []Field{},
			}
			continue
		}
		trimmed := strings.TrimSpace(raw)
		out[i] = FieldResult{
			Field: Field{Name: name, Reason: ReasonUnreadable},
			Alternatives: []Field{
				{Name: name, Value: &trimmed, Reason: ReasonNone},
			},
		}
	}
	return out
}

// aiUnavailableResults is the AIR-04 marker set: one document_ai_reading row.
func aiUnavailableResults() []FieldResult {
	return []FieldResult{{
		Field:        Field{Name: aiUnavailableField, Reason: ReasonUnreadable},
		Alternatives: []Field{},
	}}
}
