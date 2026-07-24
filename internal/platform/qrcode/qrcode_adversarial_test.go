// M5-09-01 (task-250), QA Mode B: adversarial / edge coverage added on top of
// the ten acceptance tests authored across Stages 2-3 (qrcode_test.go). Those
// prove Render/RenderBase64 satisfy the story's Test Specs table; these
// attack the gaps a spec table doesn't name -- real QR structure (not just
// "some PNG bytes came back"), the actual production payload shape, the
// exact byte-mode capacity boundary, concurrency safety, cross-process
// determinism, and pathological character classes.
package qrcode

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"strings"
	"sync"
	"testing"
)

// --- 1. Is it actually a QR code, or just a PNG? ----------------------------
//
// png.Decode succeeding (as TestRender_ReturnsDecodablePNG already checks)
// proves the bytes are a well-formed PNG. It proves NOTHING about QR
// structure -- Render could hand back a decodable PNG of a cat photo and
// that test would still pass. rsc.io/qr's own raster geometry is public and
// checkable without a barcode-scanning dependency: a real QR symbol's side
// length is 17+4*v modules for a version v in [1,40] (ISO/IEC 18004), Render
// fixes Scale=4, and rsc.io/qr's codeImage.Bounds() adds an 8-module quiet
// zone (qr.go: d := (c.Size+8)*c.Scale). So a genuine Render output's pixel
// width must equal 4*(17+4v+8) = 100+16v for some integer v in [1,40] --
// this is the property that separates a real QR raster from an arbitrary
// same-sized PNG. Expected versions below were verified empirically against
// rsc.io/qr@v0.2.0 (byte-mode payloads, level M) before being pinned here.

func TestRender_QRModuleGrid_MatchesRealQRVersionFormula(t *testing.T) {
	cases := []struct {
		n       int
		wantVer int
	}{
		{10, 1},
		{100, 6},
		{500, 17},
		{1000, 26},
		{2000, 38},
		{2331, 40}, // exactly at the byte-mode capacity ceiling
	}
	for _, c := range cases {
		payload := strings.Repeat("a", c.n)
		got, err := Render(payload)
		if err != nil {
			t.Fatalf("n=%d: Render returned error: %v", c.n, err)
		}
		img, err := png.Decode(bytes.NewReader(got))
		if err != nil {
			t.Fatalf("n=%d: png.Decode failed: %v", c.n, err)
		}
		b := img.Bounds()
		if b.Dx() != b.Dy() {
			t.Fatalf("n=%d: image is not square (%dx%d) -- a real QR symbol always is", c.n, b.Dx(), b.Dy())
		}
		if (b.Dx()-100)%16 != 0 {
			t.Fatalf("n=%d: width %dpx does not fit the 100+16v QR module formula -- this is not a valid QR raster",
				c.n, b.Dx())
		}
		gotVer := (b.Dx() - 100) / 16
		if gotVer < 1 || gotVer > 40 {
			t.Fatalf("n=%d: derived QR version %d is outside the valid [1,40] range", c.n, gotVer)
		}
		if gotVer != c.wantVer {
			t.Errorf("n=%d chars: derived QR version = %d, want %d", c.n, gotVer, c.wantVer)
		}
	}
}

func TestRender_MateriallyDifferentPayloadLengths_ProduceDifferentDimensions(t *testing.T) {
	short := strings.Repeat("a", 10)
	long := strings.Repeat("a", 2000)

	gotShort, err := Render(short)
	if err != nil {
		t.Fatalf("Render(short): %v", err)
	}
	gotLong, err := Render(long)
	if err != nil {
		t.Fatalf("Render(long): %v", err)
	}

	imgShort, err := png.Decode(bytes.NewReader(gotShort))
	if err != nil {
		t.Fatalf("decode short: %v", err)
	}
	imgLong, err := png.Decode(bytes.NewReader(gotLong))
	if err != nil {
		t.Fatalf("decode long: %v", err)
	}

	if imgShort.Bounds().Dx() == imgLong.Bounds().Dx() {
		t.Fatalf("a %d-char payload and a %d-char payload produced identically-sized QR rasters (%dpx) -- "+
			"Render is not varying the QR version with the payload's actual data",
			len(short), len(long), imgShort.Bounds().Dx())
	}
	if imgLong.Bounds().Dx() <= imgShort.Bounds().Dx() {
		t.Errorf("the longer payload (%d chars, %dpx) is not larger than the shorter one (%d chars, %dpx) -- "+
			"QR version should grow monotonically with payload length",
			len(long), imgLong.Bounds().Dx(), len(short), imgShort.Bounds().Dx())
	}
}

// --- 2. A realistic production payload --------------------------------------
//
// mbsQR mirrors internal/submission/mock_script.go's mockQR shape exactly
// (irn/csid/tin/amt/cur, mock_script.go:183-189) -- redeclared here rather
// than importing internal/submission, since a leaf package's test fixture
// should not pull in a higher-level domain package just to borrow a struct.
// The real qr_payload is base64.RawURLEncoding of this JSON shape
// (mock_script.go:241-247); this test builds one the same way and confirms
// it renders -- the input that actually reaches GetHandler in production.
type mbsQR struct {
	IRN  string `json:"irn"`
	CSID string `json:"csid"`
	TIN  string `json:"tin"`
	Amt  string `json:"amt"`
	Cur  string `json:"cur"`
}

func TestRender_RealisticMBSPayload_RendersAndDecodable(t *testing.T) {
	body, err := json.Marshal(mbsQR{
		IRN:  "INV-0042-2026-64392000-20260725",
		CSID: "MTIzNDU2Nzg5MGFiY2RlZg",
		TIN:  "12345678-0001",
		Amt:  "184250.00",
		Cur:  "NGN",
	})
	if err != nil {
		t.Fatalf("marshal mbsQR: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(body)

	got, err := Render(payload)
	if err != nil {
		t.Fatalf("Render(<realistic mbs payload>, %d chars) returned error: %v -- this is the input that "+
			"actually reaches GetHandler in production", len(payload), err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("png.Decode(<realistic mbs payload render>) failed: %v", err)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("decoded image has non-positive bounds: %+v", img.Bounds())
	}
}

// --- 3. Capacity boundary ----------------------------------------------------
//
// 2331 lowercase (byte-mode) chars is the exact ceiling at level qr.M --
// independently reconfirmed against rsc.io/qr@v0.2.0 in this worktree before
// being pinned here (matches Stage 1 validation addenda #1, task-250).

func TestRender_ByteModeCapacityBoundary_ExactlyAtCapacitySucceeds(t *testing.T) {
	payload := strings.Repeat("a", 2331)
	got, err := Render(payload)
	if err != nil {
		t.Fatalf("Render(<exactly 2331 byte-mode chars>) returned error %v, want success -- this is exactly at "+
			"the byte-mode capacity ceiling, not over it", err)
	}
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("Render(<2331 chars>) produced undecodable PNG: %v", err)
	}
}

func TestRender_ByteModeCapacityBoundary_OneOverCapacityFails(t *testing.T) {
	payload := strings.Repeat("a", 2332)
	got, err := Render(payload)
	if err == nil {
		t.Fatalf("Render(<2332 byte-mode chars>) returned a nil error (bytes=%d); want a non-nil error -- one "+
			"char past the 2331-char byte-mode capacity ceiling at level qr.M", len(got))
	}
	if got != nil {
		t.Errorf("Render(<2332 byte-mode chars>) returned non-nil bytes (%d bytes) alongside a non-nil error",
			len(got))
	}
}

// --- 4. Concurrency ------------------------------------------------------------
//
// Render is documented as holding no package-level mutable state (AC-1) --
// it runs inside GetHandler, invoked concurrently by many goroutines under a
// real HTTP server. Run with `go test -race` to catch any shared state
// rsc.io/qr itself might hide (e.g. a package-level Galois field table
// mutated during encoding).

func TestRender_ConcurrentRendersProduceIdenticalOutput(t *testing.T) {
	const goroutines = 50
	want, err := Render(samplePayload)
	if err != nil {
		t.Fatalf("reference Render: %v", err)
	}

	results := make([][]byte, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Render(samplePayload)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Render returned error: %v", i, err)
		}
		if !bytes.Equal(want, results[i]) {
			t.Errorf("goroutine %d: concurrent Render output differs from the single-goroutine reference "+
				"(%d vs %d bytes) -- Render must be safe to call concurrently with no shared state",
				i, len(results[i]), len(want))
		}
	}
}

// --- 5. Determinism across processes, not just within one --------------------
//
// TestRender_IsDeterministic (qrcode_test.go) only proves two calls in the
// SAME test run agree. It cannot catch a future rsc.io/qr bump, a Go
// image/png stdlib change, or an accidental Scale/level edit that changes
// the raster in a way that is still internally self-consistent (both calls
// in the same run would still agree with EACH OTHER, just not with what
// shipped before). This pins the actual byte-for-byte output of a fixed
// payload via a golden SHA-256 hash + length, captured against
// rsc.io/qr@v0.2.0 in this worktree -- any future change to the rendered
// bytes, for any reason, fails this test.

func TestRender_IsDeterministicAcrossProcesses_GoldenHash(t *testing.T) {
	const golden = "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"
	const wantHash = "e004b1a4d5abc1ae4ac532c0cd8bb8a88e44fb64e7e3a5763c28f65a2199523b"
	const wantLen = 977

	got, err := Render(golden)
	if err != nil {
		t.Fatalf("Render(%q): %v", golden, err)
	}
	if len(got) != wantLen {
		t.Errorf("Render(%q) produced %d bytes, want the pinned golden length %d -- the raster has changed",
			golden, len(got), wantLen)
	}
	sum := sha256.Sum256(got)
	gotHash := hex.EncodeToString(sum[:])
	if gotHash != wantHash {
		t.Errorf("Render(%q) sha256 = %s, want the pinned golden hash %s -- the raster has changed byte-for-byte "+
			"(a library bump, a Go image/png stdlib change, or an accidental Scale/level edit)",
			golden, gotHash, wantHash)
	}
}

// --- 6. Character classes ------------------------------------------------------
//
// No panics on ANY input, and exactly one of two clean outcomes: a non-nil
// error with nil bytes, or a nil error with bytes that decode as a valid
// PNG. There is no third outcome GetHandler's "log it, never 5xx" contract
// can tolerate.

func TestRender_UTF8MultibytePayload_NoPanicCleanOutcome(t *testing.T) {
	assertNoPanicCleanOutcome(t, "支払い金額100円テスト請求書-invoice-🧾")
}

func TestRender_ControlBytesPayload_NoPanicCleanOutcome(t *testing.T) {
	assertNoPanicCleanOutcome(t, "irn\x00\x01\x02csid\x1b[31mfoo\x7f")
}

func TestRender_InvalidUTF8Payload_NoPanicCleanOutcome(t *testing.T) {
	assertNoPanicCleanOutcome(t, string([]byte{0xff, 0xfe, 0xfd, 'a', 'b', 'c', 0x80, 0x81}))
}

func assertNoPanicCleanOutcome(t *testing.T, payload string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Render(%q) panicked: %v", payload, r)
		}
	}()
	got, err := Render(payload)
	if err != nil {
		if got != nil {
			t.Errorf("Render(%q) returned non-nil bytes (%d bytes) alongside a non-nil error", payload, len(got))
		}
		return
	}
	if got == nil {
		t.Fatalf("Render(%q) returned a nil error and nil bytes -- must return one or the other", payload)
	}
	if _, decErr := png.Decode(bytes.NewReader(got)); decErr != nil {
		t.Errorf("Render(%q) returned a nil error but the bytes do not decode as PNG: %v", payload, decErr)
	}
}
