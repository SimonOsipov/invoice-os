// aiimage.go: AIR-05-02 stub. Signatures only, per .ralph/plan-02.md -- every body is a
// placeholder until AIR-05-03/04 implement it. readImagesAI and the worker field land in
// AIR-05-03; callAI/aiReadings land in aireading.go with the executor's change.
package extraction

import (
	"context"
	"errors"
)

// aiImageIntro is tools/aimodeltest/run.py's IMAGE_INTRO, byte for byte. Stub value; T01 fails
// on assertion until the real text lands.
const aiImageIntro = ""

// aiImagePages keeps the first and last page: header fields sit there; middle pages carry line
// items (AIR-08).
func aiImagePages(images []PageImage) []PageImage {
	return nil
}

// readPagePNGs reads each page back by its storage key and closes every body.
func readPagePNGs(ctx context.Context, read PageObject, pages []PageImage) ([][]byte, error) {
	return nil, errors.New("not implemented")
}

// askAIPages asks once with page images. hint steers the fake only; the client never sends or
// logs it.
func askAIPages(ctx context.Context, r AIReader, pngs [][]byte, hint string) (map[string]string, bool) {
	return nil, false
}

// imageReading decides each header field from the answer alone: format check only, no region.
func imageReading(answer map[string]string) []FieldResult {
	return nil
}

// aiUnavailableResults is the AIR-04 marker set: one document_ai_reading row.
func aiUnavailableResults() []FieldResult {
	return nil
}
