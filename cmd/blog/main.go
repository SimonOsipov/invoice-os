// Command blog serves the blog SPA with crawler-readable HTML for every post URL.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
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
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		platform.Fatal(log, "blog: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, ln, h, shutdownTimeout); err != nil {
		platform.Fatal(log, "blog: %v", err)
	}
}

// shutdownTimeout matches the platform default.
const shutdownTimeout = 10 * time.Second

// run serves until ctx is done, then lets in-flight requests finish within grace.
func run(ctx context.Context, ln net.Listener, h http.Handler, grace time.Duration) error {
	srv := platform.NewServer("", h)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	return srv.Shutdown(sctx)
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
