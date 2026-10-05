package gateway

import (
	"net/http"
	"net/url"
)

// VerifyPageHandler is a Mode A stub: RESEND-02-01 replaces it with the confirm page.
func VerifyPageHandler(*url.URL) (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}), nil
}
