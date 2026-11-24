package erp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

type connectRequest struct {
	System       string `json:"system"`
	ConnectionID string `json:"connection_id"`
}

// connectResponse is the Connection plus the URL the admin visits to consent.
type connectResponse struct {
	Connection
	ConsentURL string `json:"consent_url"`
}

type listResponse struct {
	Connections []Connection `json:"connections"`
	Systems     []string     `json:"systems"`
}

// ListHandler serves GET /v1/entities/{id}/erp-connections. It calls no gate: every member who can see the client lists.
func ListHandler(list func(ctx context.Context, entityID string) ([]Connection, error), systems func() []string, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		conns, err := list(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, r, log, "list erp connections", err)
			return
		}
		if conns == nil {
			conns = []Connection{}
		}
		sys := systems()
		if sys == nil {
			sys = []string{}
		}
		writeJSON(w, http.StatusOK, listResponse{Connections: conns, Systems: sys})
	}
}

// ConnectHandler serves POST /v1/entities/{id}/erp-connections. The admin gate answers before the body is read,
// so a non-admin gets 403 whatever it sent.
func ConnectHandler(
	gate func(ctx context.Context) error,
	connect func(ctx context.Context, entityID string, in ConnectInput) (Connection, string, bool, error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := gate(r.Context()); err != nil {
			fail(w, r, log, "gate erp connect", err)
			return
		}
		var req connectRequest
		if err := jsonbody.Decode(w, r, jsonbody.DefaultLimit, &req); err != nil {
			if jsonbody.TooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		conn, consentURL, created, err := connect(r.Context(), r.PathValue("id"), ConnectInput{System: req.System, ConnectionID: req.ConnectionID})
		if err != nil {
			fail(w, r, log, "connect erp", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, connectResponse{Connection: conn, ConsentURL: consentURL})
	}
}

type saveMappingRequest struct {
	Fields []FieldChange `json:"fields"`
}

type saveLookupsRequest struct {
	Lookups []Lookup `json:"lookups"`
}

// MappingHandler serves GET /v1/entities/{id}/erp-mappings/{system}. It calls no gate.
func MappingHandler(mapping func(ctx context.Context, entityID, system string) (MappingView, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		v, err := mapping(r.Context(), r.PathValue("id"), r.PathValue("system"))
		if err != nil {
			fail(w, r, log, "read erp mapping", err)
			return
		}
		writeJSON(w, http.StatusOK, nonNilFields(v))
	}
}

// SaveMappingHandler serves PUT /v1/entities/{id}/erp-mappings/{system}; the admin gate answers before the body is read.
func SaveMappingHandler(
	gate func(ctx context.Context) error,
	save func(ctx context.Context, entityID, system string, changes []FieldChange) (MappingView, error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := gate(r.Context()); err != nil {
			fail(w, r, log, "gate erp mapping save", err)
			return
		}
		var req saveMappingRequest
		if !decodeCapped(w, r, &req) {
			return
		}
		v, err := save(r.Context(), r.PathValue("id"), r.PathValue("system"), req.Fields)
		if err != nil {
			fail(w, r, log, "save erp mapping", err)
			return
		}
		writeJSON(w, http.StatusOK, nonNilFields(v))
	}
}

// LookupsHandler serves GET /v1/entities/{id}/erp-lookups/{system}?external_key=. It calls no gate.
func LookupsHandler(lookups func(ctx context.Context, entityID, system, externalKey string) (LookupsView, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		v, err := lookups(r.Context(), r.PathValue("id"), r.PathValue("system"), r.URL.Query().Get("external_key"))
		if err != nil {
			fail(w, r, log, "read erp lookups", err)
			return
		}
		writeJSON(w, http.StatusOK, nonNilLookups(v))
	}
}

// SaveLookupsHandler serves PUT /v1/entities/{id}/erp-lookups/{system}?external_key=; the admin gate answers before the body is read.
func SaveLookupsHandler(
	gate func(ctx context.Context) error,
	save func(ctx context.Context, entityID, system, externalKey string, entries []Lookup) (LookupsView, error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := gate(r.Context()); err != nil {
			fail(w, r, log, "gate erp lookups save", err)
			return
		}
		var req saveLookupsRequest
		if !decodeCapped(w, r, &req) {
			return
		}
		v, err := save(r.Context(), r.PathValue("id"), r.PathValue("system"), r.URL.Query().Get("external_key"), req.Lookups)
		if err != nil {
			fail(w, r, log, "save erp lookups", err)
			return
		}
		writeJSON(w, http.StatusOK, nonNilLookups(v))
	}
}

// decodeCapped reads the capped JSON body into dst; false means it already answered 413 or 400.
func decodeCapped(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := jsonbody.Decode(w, r, jsonbody.DefaultLimit, dst); err != nil {
		if jsonbody.TooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

func nonNilFields(v MappingView) MappingView {
	if v.Fields == nil {
		v.Fields = []FieldView{}
	}
	return v
}

func nonNilLookups(v LookupsView) LookupsView {
	if v.Lookups == nil {
		v.Lookups = []Lookup{}
	}
	return v
}

// DisconnectHandler serves POST /v1/entities/{id}/erp-connections/{connection}/disconnect.
func DisconnectHandler(disconnect func(ctx context.Context, entityID, connectionID string) (Connection, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		conn, err := disconnect(r.Context(), r.PathValue("id"), r.PathValue("connection"))
		if err != nil {
			fail(w, r, log, "disconnect erp", err)
			return
		}
		writeJSON(w, http.StatusOK, conn)
	}
}

// OverviewHandler serves GET /v1/entities/{id}/erp-connections/{connection}/overview. It calls no gate.
func OverviewHandler(overview func(ctx context.Context, entityID, connectionID string) (Overview, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		v, err := overview(r.Context(), r.PathValue("id"), r.PathValue("connection"))
		if err != nil {
			fail(w, r, log, "read erp overview", err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

// MarksHandler serves GET /v1/entities/{id}/invoices/{invoice}/erp-marks. It calls no gate.
func MarksHandler(marks func(ctx context.Context, entityID, invoiceID string) ([]DocumentMark, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		m, err := marks(r.Context(), r.PathValue("id"), r.PathValue("invoice"))
		if err != nil {
			fail(w, r, log, "read erp marks", err)
			return
		}
		if m == nil {
			m = []DocumentMark{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"marks": m})
	}
}

// statusForErr maps a store error to its status and message; ErrNoSuchRecord covers a missing,
// invisible or out-of-scope client or connection, so all of them answer alike.
func statusForErr(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusForbidden, db.WorkspaceLockedMessage
	case errors.Is(err, ErrNotPermitted):
		return http.StatusForbidden, "only an admin can manage ERP connections"
	case errors.Is(err, ErrUnknownSystem):
		return http.StatusBadRequest, "unknown system"
	case errors.Is(err, ErrNoSuchRecord):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrAlreadyConnected):
		return http.StatusConflict, "already connected"
	case errors.Is(err, ErrKeylessPending):
		return http.StatusConflict, "another connection of this system is already waiting for consent"
	case errors.Is(err, ErrAlreadyDisconnected):
		return http.StatusConflict, "already disconnected"
	case errors.Is(err, ErrUnsupported):
		return http.StatusNotImplemented, "this system cannot connect yet"
	case errors.Is(err, ErrInvalidMapping), errors.Is(err, ErrInvalidLookup):
		return http.StatusBadRequest, err.Error()
	}
	return http.StatusInternalServerError, "internal server error"
}

// fail answers err; an unrecognised one is logged with the operation name and never a body.
func fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	status, msg := statusForErr(err)
	if status == http.StatusInternalServerError {
		log.ErrorContext(r.Context(), "erp: "+op, slog.Any("err", err))
	}
	writeError(w, status, msg)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
