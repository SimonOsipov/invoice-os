// Package qrcode is a leaf package (M5-09-01, task-250): two pure, stateless
// functions that render a payload string as a QR code PNG. No package-level
// mutable state.
//
// THIS IS A COMPILE-ENABLING STUB (Stage 2, QA Mode A). Both functions
// currently return "not implemented" -- the real body (rsc.io/qr, level
// qr.M, Code.Scale = 4, per the story's pinned library decision) is Stage 3
// (executor) work. It exists so internal/platform/qrcode/qrcode_test.go and
// internal/invoice/handlers_test.go's QR specs compile and fail RED on their
// assertions, never on a build error.
package qrcode

import "errors"

// Render encodes payload as a QR code and returns PNG bytes.
// A blank payload is an error -- there is no such thing as a QR of nothing.
func Render(payload string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// RenderBase64 is Render followed by base64.StdEncoding.
func RenderBase64(payload string) (string, error) {
	return "", errors.New("not implemented")
}
