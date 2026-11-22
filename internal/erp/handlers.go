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
	System      string `json:"system"`
	ExternalKey string `json:"external_key"`
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
	connect func(ctx context.Context, entityID string, in ConnectInput) (Connection, bool, error),
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
		conn, created, err := connect(r.Context(), r.PathValue("id"), ConnectInput{System: req.System, ExternalKey: req.ExternalKey})
		if err != nil {
			fail(w, r, log, "connect erp", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, conn)
	}
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
	case errors.Is(err, ErrUnknownExternalKey):
		return http.StatusBadRequest, "unknown external_key"
	case errors.Is(err, ErrNoSuchRecord):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrAlreadyConnected):
		return http.StatusConflict, "already connected"
	case errors.Is(err, ErrAlreadyDisconnected):
		return http.StatusConflict, "already disconnected"
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
