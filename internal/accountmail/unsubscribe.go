package accountmail

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	// UnsubscribeURL is the unsubscribe page, like LogoURL and AppURL for the other links.
	UnsubscribeURL = "https://api.ascomply.com/unsubscribe"
	// UnsubscribeMetadataKey is the GoTrue user_metadata key the confirmation and recovery mails read.
	UnsubscribeMetadataKey = "unsubscribe"

	unsubscribeQuery = "?t=ASCOMPLY_UNSUBSCRIBE_TOKEN"
)

// TokenFunc returns the unsubscribe token of one address.
type TokenFunc func(ctx context.Context, email string) (string, error)

// PoolTokens reads the token through mail_unsubscribe_token, which creates it on first use.
func PoolTokens(q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) TokenFunc {
	return func(ctx context.Context, email string) (string, error) {
		var token string
		if err := q.QueryRow(ctx, `SELECT public.mail_unsubscribe_token($1)::text`, email).Scan(&token); err != nil {
			return "", err
		}
		return token, nil
	}
}

// fillUnsubscribe replaces the placeholder with the token of m.To. It returns the new HTML and the
// token, or "" when the mail has no placeholder or the token is unavailable: that mail keeps the
// bare link, and the WARN names no address.
func fillUnsubscribe(ctx context.Context, tokens TokenFunc, m Message) (html, token string) {
	if !strings.Contains(m.HTML, unsubscribeQuery) {
		return m.HTML, ""
	}
	var err error
	if tokens == nil {
		err = errors.New("no token source")
	} else if token, err = tokens(ctx, m.To); err == nil && token == "" {
		err = errors.New("empty token")
	}
	if err != nil {
		slog.WarnContext(ctx, "accountmail: unsubscribe token unavailable, sending the bare link")
		return strings.ReplaceAll(m.HTML, unsubscribeQuery, ""), ""
	}
	return strings.ReplaceAll(m.HTML, unsubscribeQuery, "?t="+url.QueryEscape(token)), token
}
