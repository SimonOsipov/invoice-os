package gateway

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	// exportStatePrefix separates export codes from sign-in codes in the shared store.
	exportStatePrefix    = "workspace-export:"
	exportUpstreamPath   = "/api/invoice/v1/workspace/export"
	maxExportFormBytes   = 4 << 10
	exportWriteWindow    = time.Hour
	exportRefusedScript  = `document.querySelector('button').addEventListener('click', function () { history.back() })`
	exportRefusalDefault = "The export could not start. Try again."
)

// Filled by strings.NewReplacer, not html/template (TestProductionGatewayBinaryCannotMint).
//
//go:embed export_refused.html
var exportRefusedHTML string

func exportRefusalMessage(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "This download link has expired. Go back and click Download export again."
	case http.StatusUnauthorized:
		return "Your session has ended. Sign in again, then download the export."
	case http.StatusForbidden:
		return "Only an active admin can download the workspace export."
	case http.StatusConflict:
		return "An export of this workspace is already running. Wait for it to finish, then try again."
	case http.StatusServiceUnavailable:
		return "Another workspace export is running. Try again in a few minutes."
	}
	return exportRefusalDefault
}

// writeExportRefusal answers the refusal page with the given status.
func writeExportRefusal(w http.ResponseWriter, status int) {
	sum := sha256.Sum256([]byte(exportRefusedScript))
	page := strings.NewReplacer(
		"{{.Message}}", html.EscapeString(exportRefusalMessage(status)),
		"{{.Script}}", exportRefusedScript,
	).Replace(exportRefusedHTML)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-"+
		base64.StdEncoding.EncodeToString(sum[:])+"'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(page))
}

// refusalPageWriter turns a non-200 upstream answer into the export refusal page.
type refusalPageWriter struct {
	http.ResponseWriter
	refused bool
	started bool
}

func (w *refusalPageWriter) WriteHeader(code int) {
	if w.started || (code >= 100 && code < 200) {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.started = true
	if code == http.StatusOK {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.refused = true
	h := w.Header()
	requestID := h.Get(headerRequestID)
	for k := range h {
		delete(h, k)
	}
	if requestID != "" {
		h.Set(headerRequestID, requestID)
	}
	writeExportRefusal(w.ResponseWriter, code)
}

func (w *refusalPageWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if w.refused {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.NewResponseController reach the connection's Flush and write deadline.
func (w *refusalPageWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ExportCodeHandler answers POST /auth/export-code. It runs behind the verifier and the session check.
func ExportCodeHandler(store Handoffs, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !stateShape.MatchString(in.State) {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}
		bearer, _ := auth.BearerToken(r) // the verifier already admitted it
		answer, _ := json.Marshal(map[string]string{"access_token": bearer})
		code, ok := store.Put(string(answer), sha256.Sum256([]byte(exportStatePrefix+in.State)))
		if !ok {
			log.WarnContext(r.Context(), "export code: code store is full")
			writeError(w, http.StatusServiceUnavailable, "the export is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	})
}

// ExportDownloadHandler answers POST /auth/export-download: it redeems the form's code for the stored
// token and serves GET /api/invoice/v1/workspace/export through api, verifier and session check included.
func ExportDownloadHandler(store Handoffs, api http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxExportFormBytes)
		if err := r.ParseForm(); err != nil {
			writeExportRefusal(w, http.StatusBadRequest)
			return
		}
		code, state := r.PostForm.Get("code"), r.PostForm.Get("state")
		if code == "" || !stateShape.MatchString(state) {
			writeExportRefusal(w, http.StatusBadRequest)
			return
		}
		answer, ok := store.Take(code, exportStatePrefix+state)
		if !ok {
			writeExportRefusal(w, http.StatusBadRequest)
			return
		}
		var stored struct {
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal([]byte(answer), &stored) != nil || stored.AccessToken == "" {
			log.WarnContext(r.Context(), "export download: stored answer is unusable")
			writeExportRefusal(w, http.StatusBadRequest)
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exportWriteWindow))

		r2 := r.Clone(r.Context())
		r2.Method = http.MethodGet
		r2.URL = &url.URL{Path: exportUpstreamPath}
		r2.Header = http.Header{"Authorization": {"Bearer " + stored.AccessToken}}
		r2.Body = http.NoBody
		r2.ContentLength = 0
		r2.Form, r2.PostForm = nil, nil
		api.ServeHTTP(&refusalPageWriter{ResponseWriter: w}, r2)
	})
}
