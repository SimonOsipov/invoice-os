package gateway

import (
	"bytes"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/packages/design-tokens/v2/assets"
)

// MailTemplate serves accountmail.Template(name) as text/html. An unknown name is an error, so boot fails on it.
func MailTemplate(name string) (http.Handler, error) {
	body, err := accountmail.Template(name)
	if err != nil {
		return nil, err
	}
	return staticBytes("text/html; charset=utf-8", body), nil
}

// MailLogo serves the account-mail mark as image/png.
func MailLogo() http.Handler { return staticBytes("image/png", assets.Mark) }

// staticBytes serves body with a fixed content type; ServeContent answers HEAD without a body.
func staticBytes(contentType string, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	})
}
