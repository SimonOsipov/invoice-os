package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// qaERPBootEnv is the ERP env preview.sh gives a preview or nightly worker, which refuses to boot without it.
// The stub serves the project id and region, so the boot reads no real metadata server.
func qaERPBootEnv(t *testing.T, deployEnv string) map[string]string {
	t.Helper()
	if deployEnv != "nightly" && !strings.HasPrefix(deployEnv, "pr-") {
		return nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Metadata-Flavor", "Google")
		switch r.URL.Path {
		case "/computeMetadata/v1/project/project-id":
			_, _ = io.WriteString(w, "erp-boot-project")
		case "/computeMetadata/v1/instance/region":
			_, _ = io.WriteString(w, "projects/1/regions/africa-south1")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return map[string]string{
		"ERP_REDIRECT_URL":  "https://gateway.invalid/erp/callback",
		"ERP_SECRET_SCOPE":  "slot-1",
		"GCE_METADATA_HOST": strings.TrimPrefix(srv.URL, "http://"),
	}
}
