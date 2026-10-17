---
paths:
  - "internal/blog/**"
  - "migrations/*blog*"
---
# Blog rules

- Routes live under `/v1/staff/blog/` on notifications.
- Every write takes the `blog` advisory lock and records one `RecordStaff` row per change from the `Event*` constants, plus a `pinned` / `unpinned` row per featured flag it changes.
- The author check reads `staff_read_blog_authors` and stores `author_name`.
- `blog_posts_one_featured` holds one pin.
