// Command blog serves the blog SPA with crawler-readable HTML for every post URL.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/blogweb"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func main() {
	defer platform.ReportBootPanic()
	serve(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

func serve(log *slog.Logger) {
	port, gateway, err := config(os.Getenv)
	if err != nil {
		platform.Fatal(log, "blog: config: %v", err)
	}
	h, err := blogweb.New(blogweb.Config{
		Root:    "/srv",
		Gateway: gateway,
		Client:  &http.Client{},
		Now:     time.Now,
		Log:     log,
	})
	if err != nil {
		platform.Fatal(log, "blog: %v", err)
	}
	srv := &http.Server{Addr: ":" + port, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		platform.Fatal(log, "blog: %v", err)
	}
}

func config(getenv func(string) string) (port string, gateway *url.URL, err error) {
	port = getenv("PORT")
	if port == "" {
		port = "8080"
	}
	u, perr := url.Parse(getenv("VITE_GATEWAY_URL"))
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", nil, errors.New("VITE_GATEWAY_URL must be an absolute http(s) URL")
	}
	return port, u, nil
}
