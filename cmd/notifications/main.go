// Command notifications is the 07 Notifications context service: it takes contact intake
// from the gateway and queues each delivery to HubSpot and Resend; cmd/worker runs them.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/blog"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("notifications")
	if err != nil {
		platform.Fatal(slog.Default(), "notifications: startup: %v", err)
	}

	ctx := context.Background()
	mode, _, _, err := notifications.DeliveryClients(os.Getenv, platform.DeployEnv(), notifications.RealTransport())
	if err != nil {
		platform.Fatal(app.Logger, "notifications: %v", err)
	}
	platform.Contacts = string(mode)
	nl, err := notifications.NewsletterClients(os.Getenv, platform.DeployEnv(), mode, notifications.RealTransport())
	if err != nil {
		platform.Fatal(app.Logger, "notifications: %v", err)
	}

	pool, err := db.NewPool(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "notifications: db pool: %v", err)
	}
	defer pool.Close()
	app.Ready("database", pool.Ping)

	// Insert-only: cmd/worker delivers every contact job and purges expired blog requests.
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		platform.Fatal(app.Logger, "notifications: queue: %v", err)
	}

	store := notifications.NewStore(pool, q.River())
	app.Mux.HandleFunc("POST /internal/contacts/registrants", notifications.RegistrantsHandler(store, app.Logger))
	app.Mux.HandleFunc("POST /internal/contacts/demo-requests", notifications.DemoRequestsHandler(store, app.Logger))
	app.Mux.HandleFunc("GET /v1/contacts/me", notifications.MeHandler(store, app.Logger))

	newsletterStore := notifications.NewNewsletterStore(pool, q.River())
	newsletter := notifications.Newsletter{Store: newsletterStore, Sender: nl.Sender, Resend: nl.Resend, Enabled: nl.Enabled}

	blogStore := blog.NewStore(pool)
	app.Mux.HandleFunc("GET /v1/staff/blog/categories", blog.ListCategoriesHandler(blogStore.ListCategories, app.Logger))
	app.Mux.HandleFunc("POST /v1/staff/blog/categories", blog.AddCategoryHandler(blogStore.AddCategory, app.Logger))
	app.Mux.HandleFunc("PUT /v1/staff/blog/categories/{id}", blog.UpdateCategoryHandler(blogStore.UpdateCategory, app.Logger))
	app.Mux.HandleFunc("POST /v1/staff/blog/categories/{id}/move", blog.MoveCategoryHandler(blogStore.MoveCategory, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/staff/blog/categories/{id}", blog.DeleteCategoryHandler(blogStore.DeleteCategory, app.Logger))
	app.Mux.HandleFunc("GET /v1/staff/blog/authors", blog.AuthorsHandler(blogStore.Authors, app.Logger))
	app.Mux.HandleFunc("GET /v1/staff/blog/posts", blog.ListPostsHandler(blogStore.ListPosts, app.Logger))
	app.Mux.HandleFunc("POST /v1/staff/blog/posts", blog.CreatePostHandler(blogStore.CreatePost, app.Logger))
	app.Mux.HandleFunc("PUT /v1/staff/blog/posts/{id}", blog.UpdatePostHandler(blogStore.UpdatePost, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/staff/blog/posts/{id}", blog.DeletePostHandler(blogStore.DeletePost, app.Logger))
	app.Mux.HandleFunc("PUT /v1/staff/blog/posts/{id}/featured", blog.SetFeaturedHandler(blogStore.SetFeatured, app.Logger))
	app.Mux.HandleFunc("PUT /v1/staff/blog/posts/{id}/cover", blog.SetCoverHandler(blogStore.SetCover, app.Logger))
	app.Mux.HandleFunc("GET /v1/staff/blog/posts/{id}/cover", blog.GetCoverHandler(blogStore.GetCover, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/staff/blog/posts/{id}/cover", blog.RemoveCoverHandler(blogStore.RemoveCover, app.Logger))

	// Stub endpoint — proves the service builds, boots, and routes end to end.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"notifications","status":"ok"}`))
	})

	app.Mux.HandleFunc("GET /internal/blog/categories", blog.PublicCategoriesHandler(blogStore.PublicCategories, app.Logger))
	app.Mux.HandleFunc("GET /internal/blog/posts", blog.PublicPostsHandler(blogStore.PublicPosts, app.Logger))
	app.Mux.HandleFunc("GET /internal/blog/posts/{id}", blog.PublicPostHandler(blogStore.PublicPost, app.Logger))
	app.Mux.HandleFunc("GET /internal/blog/covers/{id}/{version}", blog.PublicCoverHandler(blogStore.PublicCover, app.Logger))
	app.Mux.HandleFunc("POST /internal/blog/subscriptions", notifications.SubscribeHandler(newsletter, app.Logger))
	app.Mux.HandleFunc("POST /internal/blog/subscriptions/confirm", notifications.ConfirmHandler(newsletter, app.Logger))

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"))

	if err := app.Run(ctx); err != nil {
		platform.Fatal(app.Logger, "notifications: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "notifications: %s is required", key)
	}
	return v
}
