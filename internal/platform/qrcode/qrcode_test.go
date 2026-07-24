// M5-09-01 (task-250): RED acceptance tests for Render/RenderBase64,
// authored against the not-implemented stub in qrcode.go (both functions
// currently return errors.New("not implemented")). Every assertion below
// currently fails on the returned error / a decode failure -- never on a
// build error -- and should flip to green once the executor wires
// rsc.io/qr (qr.Encode, level qr.M, then Code.Scale = 4, then (*Code).PNG())
// per the story's pinned library decision.
//
// Two fixtures deliberately diverge from the story's original Test Specs
// table (Stage 1 validation addenda, task-250, verified against
// rsc.io/qr@v0.2.0 in an out-of-repo probe module):
//
//   - The over-capacity fixture is byte-mode (overlongBytePayload, all
//     lowercase), not all-digits. qr.Encode picks the densest encoding mode
//     that fits the input's character class (qr.go:31-41): numeric maxes out
//     at 5596 chars, alphanumeric at 3391, byte at 2331 -- all at level M.
//     A 4000-char ALL-DIGIT payload sits comfortably inside the numeric
//     ceiling and encodes successfully, so it is not a valid over-capacity
//     fixture. 4000 lowercase letters (byte mode) sits well past the
//     2331-char ceiling, and is also the mode a real base64url MBS payload
//     lands in.
//   - The blank-payload test (TestRender_BlankPayloadIsAnError) pins a
//     contract Render itself must enforce, not one rsc.io/qr provides:
//     qr.Encode("", qr.M) SUCCEEDS, returning a valid, scannable 21x21
//     empty QR. See that test's own doc comment for the exact guard
//     required.
package qrcode

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
)

// samplePayload is a realistic base64url-shaped MBS QR payload fixture.
// Lowercase letters and '.'/'-' put it in byte mode (rsc.io/qr's
// densest-fit rule) -- the same mode a real payload lands in -- well inside
// the 2331-char byte-mode ceiling at level M.
const samplePayload = "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"

// overlongBytePayload is 4000 lowercase letters -- byte mode, well past the
// 2331-char byte-mode ceiling at level qr.M (Stage 1 validation addenda #1).
// Deliberately NOT all-digits/all-uppercase: those character classes fit
// comfortably within their own (much higher) capacity ceilings and would
// encode successfully, falsifying the test's own premise.
var overlongBytePayload = strings.Repeat("a", 4000)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func TestRender_ReturnsDecodablePNG(t *testing.T) {
	got, err := Render(samplePayload)
	if err != nil {
		t.Fatalf("Render(%q) returned error %v, want a decodable PNG", samplePayload, err)
	}
	if !bytes.HasPrefix(got, pngMagic) {
		t.Fatalf("Render output (%d bytes) does not start with the PNG magic bytes \\x89PNG\\r\\n\\x1a\\n", len(got))
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("png.Decode(Render(%q)) failed: %v", samplePayload, err)
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Fatalf("decoded image has non-positive bounds: %+v", bounds)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	a, errA := Render(samplePayload)
	if errA != nil {
		t.Fatalf("first Render(%q) call returned error: %v", samplePayload, errA)
	}
	b, errB := Render(samplePayload)
	if errB != nil {
		t.Fatalf("second Render(%q) call returned error: %v", samplePayload, errB)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Render(%q) is not deterministic: two calls produced different byte slices (%d vs %d bytes)",
			samplePayload, len(a), len(b))
	}
}

// TestRender_BlankPayloadIsAnError pins a contract Render itself must
// enforce. rsc.io/qr's own qr.Encode("", qr.M) SUCCEEDS -- it returns a
// valid, scannable 21x21 version-1 code, and Scale=4 + PNG() yields a
// perfectly decodable (if useless) 528-byte PNG (Stage 1 validation addenda
// #3). So the only way this test goes green is Render adding its own
// `if payload == "" { return nil, err }` guard BEFORE ever calling
// qr.Encode. If a future change makes this test pass by any other route, or
// the fix under consideration is to weaken/remove this assertion instead of
// adding that guard, that is the wrong fix -- a blank payload silently
// producing a scannable-but-empty QR is precisely the bug this test exists
// to prevent.
func TestRender_BlankPayloadIsAnError(t *testing.T) {
	got, err := Render("")
	if err == nil {
		t.Fatalf("Render(\"\") returned a nil error (bytes=%v); want a non-nil error -- rsc.io/qr's own "+
			"qr.Encode(\"\", qr.M) happily encodes a blank payload into a valid empty QR, so Render must "+
			"reject it with its own guard rather than delegating to the library", got)
	}
	if got != nil {
		t.Errorf("Render(\"\") returned non-nil bytes (%d bytes) alongside a non-nil error; want nil bytes", len(got))
	}
}

// TestRender_OverlongPayloadIsAnError uses overlongBytePayload (byte-mode),
// not the story's original all-digit "4000 chars" wording -- see the
// package doc comment and Stage 1 validation addenda #1. rsc.io/qr's
// over-capacity path returns a plain error after exhausting
// coding.MaxVersion (addenda #2) -- no panic, so no recover() is needed
// here or in Render.
func TestRender_OverlongPayloadIsAnError(t *testing.T) {
	got, err := Render(overlongBytePayload)
	if err == nil {
		t.Fatalf("Render(<%d byte-mode chars>) returned a nil error (bytes=%d); want a non-nil error -- "+
			"the payload is well past the 2331-char byte-mode ceiling at level qr.M",
			len(overlongBytePayload), len(got))
	}
	if got != nil {
		t.Errorf("Render(<over-capacity payload>) returned non-nil bytes (%d bytes) alongside a non-nil error; want nil bytes", len(got))
	}
}

func TestRenderBase64_RoundTripsToRender(t *testing.T) {
	wantBytes, err := Render(samplePayload)
	if err != nil {
		t.Fatalf("Render(%q) returned error: %v", samplePayload, err)
	}
	gotB64, err := RenderBase64(samplePayload)
	if err != nil {
		t.Fatalf("RenderBase64(%q) returned error: %v", samplePayload, err)
	}
	gotBytes, err := base64.StdEncoding.DecodeString(gotB64)
	if err != nil {
		t.Fatalf("base64.StdEncoding.DecodeString(RenderBase64(%q)) failed: %v -- RenderBase64 must use "+
			"base64.StdEncoding (padded, standard alphabet, the encoding a data:image/png;base64, URI "+
			"requires), not RawURL", samplePayload, err)
	}
	if !bytes.Equal(wantBytes, gotBytes) {
		t.Fatalf("RenderBase64(%q) does not round-trip to Render's bytes: got %d bytes, want %d bytes",
			samplePayload, len(gotBytes), len(wantBytes))
	}
}
