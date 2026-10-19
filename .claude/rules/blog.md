---
paths:
  - "internal/blog/**"
  - "internal/gateway/blog*.go"
  - "migrations/*blog*"
  - "cmd/blog/**"
  - "internal/blogweb/**"
  - "frontend/blog/Dockerfile"
  - "frontend/blog/index.html"
---
# Blog rules

- Staff routes live under `/v1/staff/blog/` on notifications.
- Public reads live under `/internal/blog/` on notifications and under `/blog/` on the gateway. They return only posts with `status = 'published'` and `publish_date <=` `LagosDate(now)`.
- Read public content only through `readPublic`, a READ ONLY transaction.
- Set a public `Cache-Control` only in `internal/gateway/blog.go`.
- Every write takes the `blog` advisory lock and records one `RecordStaff` row per change from the `Event*` constants, plus a `pinned` / `unpinned` row per featured flag it changes.
- The author check reads `staff_read_blog_authors` and stores `author_name`.
- `blog_posts_one_featured` holds one pin.
- Serve the blog through `cmd/blog`, not the shared `Caddyfile`.
- Read posts in `internal/blogweb` only from the gateway's public `/blog/` routes.
- Render every database string in `internal/blogweb` with `html/template` or `encoding/xml`.
- Keep one `<title>` and one `<div id="root"></div>` in `frontend/blog/index.html`, and no description, canonical, robots or Open Graph tag; the server writes those.
- Let the SPA leave `<title>` and the meta tags alone; the server owns them.
