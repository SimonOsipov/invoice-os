// Package qrcode is a leaf package (M5-09-01, task-250): two pure, stateless
// functions that render a payload string as a QR code PNG. No package-level
// mutable state.
package qrcode

import (
	"encoding/base64"
	"errors"

	"rsc.io/qr"
)

// Render encodes payload as a QR code and returns PNG bytes.
// A blank payload is an error -- there is no such thing as a QR of nothing.
//
// rsc.io/qr's own qr.Encode("", qr.M) SUCCEEDS -- it happily returns a valid,
// scannable 21x21 empty QR code -- so this guard is enforced here, before
// qr.Encode is ever called, not delegated to the library (Stage 1 validation
// addenda #3, task-250).
func Render(payload string) ([]byte, error) {
	if payload == "" {
		return nil, errors.New("qrcode: payload must not be blank")
	}

	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return nil, err
	}
	code.Scale = 4

	return code.PNG(), nil
}

// RenderBase64 is Render followed by base64.StdEncoding.EncodeToString --
// deliberately StdEncoding rather than the repo-wide RawURLEncoding
// convention used for identifiers: a `data:image/png;base64,` URI is defined
// against standard, padded base64, which a browser could not decode from
// RawURL's unpadded, URL-safe alphabet.
func RenderBase64(payload string) (string, error) {
	png, err := Render(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(png), nil
}
