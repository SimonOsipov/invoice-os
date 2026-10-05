package main

import (
	"io"
	"net/http"
)

// RunMailTemplateCheck is a stub: RESEND-01-04 implements it.
func RunMailTemplateCheck(client *http.Client, urls []string, out io.Writer) int {
	return 1
}

// RunMailLogoCheck is a stub: RESEND-01-04 implements it.
func RunMailLogoCheck(client *http.Client, url string, out io.Writer) int {
	return 1
}
