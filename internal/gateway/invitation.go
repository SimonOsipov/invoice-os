package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// InvitationPreview is what a token holder sees before signing in.
type InvitationPreview struct{ Workspace, Role, Email string }

// ErrInvitationNotValid means the token names no live invite.
var ErrInvitationNotValid = errors.New("gateway: invitation is not valid")

// InvitationPreviewer looks up the live invite a token names.
type InvitationPreviewer func(ctx context.Context, token string) (InvitationPreview, error)

// invitationPreviewTimeout bounds each tenancy call; tests shorten it.
var invitationPreviewTimeout = 5 * time.Second

var errInvitationStub = errors.New("gateway: invitation routes are not implemented")

// stub: the test-first red build; the implementation replaces every body here.
func invitationStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}

// NewHTTPInvitationPreviewer is a stub.
func NewHTTPInvitationPreviewer(base *url.URL, client *http.Client, gatewayToken string) InvitationPreviewer {
	return func(context.Context, string) (InvitationPreview, error) {
		return InvitationPreview{}, errInvitationStub
	}
}

// InvitationHandler is a stub.
func InvitationHandler(preview InvitationPreviewer, log *slog.Logger) http.Handler {
	return invitationStub()
}

// InvitationRegisterHandler is a stub.
func InvitationRegisterHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perIP *SignInThrottle, enforce bool, log *slog.Logger, preview InvitationPreviewer) http.Handler {
	return invitationStub()
}
