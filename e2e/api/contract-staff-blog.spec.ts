// The staff blog contract over the wire: categories, posts, pin and cover behave as specified, the 409, 415 and 422
// refusals answer, the seeded posts are listed, and a customer token is refused on all 14 routes. The spec removes
// what it creates and restores the featured post.
import { test, expect } from '@playwright/test'
import { login, provisionStaffAccount, rawFetch, signInSession, apiBase, PERSONAS } from './client'
import type { StaffAccount } from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

const BASE = '/api/notifications/v1/staff/blog'

// MsgCategoryNameTaken, MsgFixToPublish and MsgCoverType in internal/blog/blog.go.
const CATEGORY_NAME_TAKEN_MESSAGE = 'A category with this name already exists.'
const FIX_TO_PUBLISH_MESSAGE = 'Fix the highlighted fields to publish'
const COVER_TYPE_MESSAGE = 'Choose a JPG or PNG image'
// The display_name provisionStaffAccount grants; the stored byline of a named author.
const STAFF_DISPLAY_NAME = 'ASComply staff'

// blogSeedPosts in internal/platform/db/blog_seed_rls_test.go.
const SEEDED_POST_IDS = [
  'fifty-more-pilot', 'what-changes', 'recognition-learns', 'sage-connector', 'phased-rollout',
  'approval-thresholds', 'sealed-rules', 'lagos-office', 'tin-errors', 'submission-retries',
  'firm-portfolio', 'csv-import', 'si-app-partners', 'lagos-freight', 'evidence-bundles',
  'readiness-assessment', 'records-retention', 'audit-log', 'consultant-programme', 'sahara-foods',
]

const PNG_1X1 = Uint8Array.from(
  Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64'),
)
const GIF_BYTES = Uint8Array.from(Buffer.from('GIF89a'))

type Category = { id: string; name: string; post_count: number }
type Post = { id: string; category_id: string | null; status: string; featured: boolean; author_name: string | null; cover: { content_type: string } | null }
type Author = { user_id: string; display_name: string | null; email: string | null }

// rawFetch parses every answer as JSON, so a cover read goes through a bare fetch as well.
async function getCover(token: string, id: string): Promise<{ status: number; type: string | null; bytes: Uint8Array; body: unknown }> {
  const res = await fetch(`${apiBase()}${BASE}/posts/${id}/cover`, { headers: { Authorization: `Bearer ${token}` } })
  const raw = new Uint8Array(await res.arrayBuffer())
  let body: unknown
  try {
    body = JSON.parse(new TextDecoder().decode(raw))
  } catch {
    body = undefined
  }
  return { status: res.status, type: res.headers.get('content-type'), bytes: raw, body }
}

// rawFetch JSON-encodes every body, so a cover goes through a bare fetch (previewFetch in contract-import.spec.ts does the same).
async function putCover(token: string, id: string, bytes: Uint8Array<ArrayBuffer>): Promise<RawResult> {
  const res = await fetch(`${apiBase()}${BASE}/posts/${id}/cover`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${token}` },
    body: bytes,
  })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

const categoryInput = (name: string) => ({ name, headline: '', intro: '', icon: 'package', tone: 0 })

test.describe.configure({ mode: 'serial' })

test.describe('staff blog (API E2E, over the deployed gateway)', () => {
  let customer: string
  let staff: string
  let account: StaffAccount
  let catA: Category
  let catB: Category
  const postIds: string[] = []
  let previousFeatured: Post | undefined
  let pinnedId: string | undefined
  let tag: string

  const auth = (token: string) => ({ Authorization: `Bearer ${token}` })
  const get = (path: string) => rawFetch(`${BASE}${path}`, { headers: auth(staff) })
  const send = (method: string, path: string, body?: unknown) => rawFetch(`${BASE}${path}`, { method, headers: auth(staff), body })
  const listPosts = async (): Promise<Post[]> => {
    const res = await get('/posts')
    expect(res.status, 'staff blog posts list').toBe(200)
    return (res.body as { posts: Post[] }).posts
  }
  const listCategories = async (): Promise<Category[]> => {
    const res = await get('/categories')
    expect(res.status, 'staff blog categories list').toBe(200)
    return (res.body as { categories: Category[] }).categories
  }

  test.beforeAll(async () => {
    tag = crypto.randomUUID().replace(/-/g, '').slice(0, 10)
    customer = await login(PERSONAS.A)
    account = await provisionStaffAccount('staff-blog')
    staff = (await signInSession(account.email, account.password)).access_token
  })

  test.afterAll(async () => {
    if (!staff) return
    // Restore the pin before the pinned post goes.
    if (pinnedId) {
      if (previousFeatured) await send('PUT', `/posts/${previousFeatured.id}/featured`, { featured: true })
      else await send('PUT', `/posts/${pinnedId}/featured`, { featured: false })
    }
    for (const id of postIds) await send('DELETE', `/posts/${id}`)
    for (const cat of [catA, catB]) if (cat) await send('DELETE', `/categories/${cat.id}`)
  })

  test('staff blog: a customer token is refused on every route', async () => {
    // Unknown ids and bodies that would fail validation: a route that let the customer through would write nothing.
    const post = 'e2e-no-such-post'
    const cat = 'e2e-no-such-category'
    const routes: [string, string, unknown?][] = [
      ['GET', '/categories'],
      ['POST', '/categories', {}],
      ['PUT', `/categories/${cat}`, {}],
      ['POST', `/categories/${cat}/move`, { direction: 'sideways' }],
      ['DELETE', `/categories/${cat}`],
      ['GET', '/authors'],
      ['GET', '/posts'],
      ['POST', '/posts', { status: 'bogus' }],
      ['PUT', `/posts/${post}`, { status: 'bogus' }],
      ['DELETE', `/posts/${post}`],
      ['PUT', `/posts/${post}/featured`, { featured: false }],
      ['DELETE', `/posts/${post}/cover`],
      ['GET', `/posts/${post}/cover`],
    ]
    for (const [method, path, body] of routes) {
      const res = await rawFetch(`${BASE}${path}`, { method, headers: auth(customer), body })
      assertErrorEnvelope(res, 403, `customer ${method} ${path}`)
    }
    assertErrorEnvelope(await putCover(customer, post, PNG_1X1), 403, `customer PUT /posts/${post}/cover`)
  })

  test('staff blog: the author list holds the staff account', async () => {
    const res = await get('/authors')
    expect(res.status, 'staff blog authors').toBe(200)
    const authors = (res.body as { authors: Author[] }).authors
    expect(authors).toContainEqual({ user_id: account.userId, display_name: STAFF_DISPLAY_NAME, email: account.email })
  })

  test('staff blog: categories add, refuse a duplicate, move and delete with a target', async () => {
    const nameA = `e2e blog ${tag} a`
    const nameB = `e2e blog ${tag} b`
    const a = await send('POST', '/categories', categoryInput(nameA))
    expect(a.status, 'add category A').toBe(201)
    catA = a.body as Category
    const b = await send('POST', '/categories', categoryInput(nameB))
    expect(b.status, 'add category B').toBe(201)
    catB = b.body as Category

    const dup = await send('POST', '/categories', categoryInput(nameA.toUpperCase()))
    assertErrorEnvelope(dup, 409, 'duplicate category name')
    expect((dup.body as { error: string }).error).toBe(CATEGORY_NAME_TAKEN_MESSAGE)

    const moved = await send('POST', `/categories/${catB.id}/move`, { direction: 'up' })
    expect(moved.status, 'move category B up').toBe(200)
    expect((moved.body as { moved: boolean }).moved).toBe(true)
    const ids = (await listCategories()).map((c) => c.id)
    expect(ids.indexOf(catB.id), 'B precedes A after the move').toBeGreaterThanOrEqual(0)
    expect(ids.indexOf(catB.id)).toBeLessThan(ids.indexOf(catA.id))

    const created = await send('POST', '/posts', { title: `e2e blog moved ${tag}`, category_id: catA.id, status: 'draft', body: [] })
    expect(created.status, 'create a draft in A').toBe(201)
    const movedPost = (created.body as Post).id
    postIds.push(movedPost)

    const del = await send('DELETE', `/categories/${catA.id}?move_to=${catB.id}`)
    expect(del.status, 'delete A with a target').toBe(200)
    expect(del.body).toEqual({ moved_posts: 1, move_to: catB.id })
    expect((await listPosts()).find((p) => p.id === movedPost)?.category_id, 'the post moved to B').toBe(catB.id)

    const delPost = await send('DELETE', `/posts/${movedPost}`)
    expect(delPost.status, 'delete the post').toBe(204)
    expect((await listPosts()).map((p) => p.id), 'the deleted post is gone').not.toContain(movedPost)
    assertErrorEnvelope(await send('DELETE', `/posts/${movedPost}`), 404, 'delete the post again')
  })

  test('staff blog: a post saves as a draft, refuses an incomplete publish, then publishes', async () => {
    const title = `e2e blog post ${tag}`
    const created = await send('POST', '/posts', { title, status: 'draft', body: [] })
    expect(created.status, 'create a draft with a title only').toBe(201)
    const post = created.body as Post
    expect(post.id, 'the id is the slug of the title').toBe(`e2e-blog-post-${tag}`)
    expect(post.status).toBe('draft')
    postIds.push(post.id)

    const full = { title, summary: 'An e2e summary.', category_id: catB.id, publish_date: '2026-10-01', author_id: account.userId, body: [{ type: 'p', text: 'An e2e paragraph.' }] }
    const refused = await send('PUT', `/posts/${post.id}`, { ...full, summary: '', status: 'published' })
    expect(refused.status, 'publish without a summary').toBe(422)
    const refusedBody = refused.body as { error: string; fields: Record<string, string> }
    expect(refusedBody.error).toBe(FIX_TO_PUBLISH_MESSAGE)
    expect(Object.keys(refusedBody.fields)).toContain('summary')

    const published = await send('PUT', `/posts/${post.id}`, { ...full, status: 'published' })
    expect(published.status, 'publish a complete post').toBe(200)
    const out = published.body as Post
    expect(out.status).toBe('published')
    expect(out.author_name).toBe(STAFF_DISPLAY_NAME)
    expect(JSON.stringify(published.body), 'the author email stays off the post').not.toContain(account.email)
  })

  test('staff blog: pin and cover', async () => {
    const id = postIds[postIds.length - 1]
    previousFeatured = (await listPosts()).find((p) => p.featured)
    const pin = await send('PUT', `/posts/${id}/featured`, { featured: true })
    expect(pin.status, 'pin the post').toBe(200)
    pinnedId = id
    expect(pin.body).toEqual({ id, featured: true, unpinned: previousFeatured?.id ?? null })
    const pinned = (await listPosts()).filter((p) => p.featured).map((p) => p.id)
    expect(pinned, 'one post is pinned').toEqual([id])

    const cover = await putCover(staff, id, PNG_1X1)
    expect(cover.status, 'put a PNG cover').toBe(200)
    expect((cover.body as { content_type: string }).content_type).toBe('image/png')
    expect((await listPosts()).find((p) => p.id === id)?.cover?.content_type, 'the listing carries the cover').toBe('image/png')
    const read = await getCover(staff, id)
    expect(read.status, 'read the saved cover').toBe(200)
    expect(read.type, 'the cover content type').toBe('image/png')
    expect(Buffer.from(read.bytes).equals(Buffer.from(PNG_1X1)), 'the cover bytes round trip').toBe(true)

    const gif = await putCover(staff, id, GIF_BYTES)
    assertErrorEnvelope(gif, 415, 'GIF cover')
    expect((gif.body as { error: string }).error).toBe(COVER_TYPE_MESSAGE)

    const removed = await send('DELETE', `/posts/${id}/cover`)
    expect(removed.status, 'remove the cover').toBe(204)
    expect((await listPosts()).find((p) => p.id === id)?.cover, 'the cover is gone').toBeNull()
    const gone = await getCover(staff, id)
    assertErrorEnvelope({ status: gone.status, body: gone.body }, 404, 'GET the removed cover')
  })

  test('staff blog: the PR env lists the seeded posts as published', async () => {
    const byId = new Map((await listPosts()).map((p) => [p.id, p]))
    for (const id of SEEDED_POST_IDS) expect(byId.get(id)?.status, `seeded post ${id}`).toBe('published')
  })
})
