package gateway

import "net/http"

func notBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
}

// MailTemplate serves accountmail.Template(name) as text/html.
func MailTemplate(name string) (http.Handler, error) { return notBuilt(), nil }

// MailLogo serves the account-mail mark as image/png.
func MailLogo() http.Handler { return notBuilt() }
