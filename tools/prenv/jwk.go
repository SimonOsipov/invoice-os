package main

import (
	"fmt"
	"io"
)

// RunJWKES256 writes a one-element JSON array holding a fresh P-256 private
// JWK to out and returns the process exit code.
func RunJWKES256(out io.Writer) int {
	fmt.Fprintln(out, "jwk-es256: not implemented")
	return 2
}

// RunJWKCheck reads a JWK array from in and returns 0 only for exactly one
// ES256 signing key. It never echoes the key.
func RunJWKCheck(in io.Reader, out io.Writer) int {
	fmt.Fprintln(out, "jwk-check: not implemented")
	return 2
}
