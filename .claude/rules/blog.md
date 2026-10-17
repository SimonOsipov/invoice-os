---
paths:
  - "internal/blog/**"
  - "internal/gateway/blog*.go"
  - "migrations/*blog*"
---
# Blog rules

- Staff routes live under `/v1/staff/blog/` on notifications.
- Public reads live under `/internal/blog/` on notifications and under `/blog/` on the gateway. They return only posts with `status = 'published'` and `publish_date <=` `LagosDate(now)`.
- Read public content only through `readPublic`, a READ ONLY transaction.
- Set a public `Cache-Control` only in `internal/gateway/blog.go`.
- Every write takes the `blog` advisory lock and records one `RecordStaff` row per change from the `Event*` constants, plus a `pinned` / `unpinned` row per featured flag it changes.
- The author check reads `staff_read_blog_authors` and stores `author_name`.
- `blog_posts_one_featured` holds one pin.
