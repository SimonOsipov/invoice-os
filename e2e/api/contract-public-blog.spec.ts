// The public blog over the deployed gateway: anonymous reads, the uniform 404, covers, CORS and publish visibility.
// Staff setup goes through the staff routes; the spec removes what it creates and never pins a post.
import { test, expect } from '@playwright/test'
import { provisionStaffAccount, rawFetch, signInSession, apiBase } from './client'
import type { StaffAccount } from './client'
import { resolveTarget } from '../targets'

const STAFF = '/api/notifications/v1/staff/blog'
const SHORT_CACHE = 'public, max-age=60'
const COVER_CACHE = 'public, max-age=31536000, immutable'
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
// One trailing byte: other bytes, still a PNG by signature.
const PNG_1X1_B = Uint8Array.from([...PNG_1X1, 0])

// Lagos is UTC+1: yesterday's Lagos date is never today's, so no case sits on the midnight boundary.
const VISIBLE_DATE = new Date(Date.now() + 3600e3 - 86400e3).toISOString().slice(0, 10)
const HIDDEN_DATE = '3001-01-01'

type Category = { id: string; name: string; post_count: number }
type PublicPost = Record<string, unknown> & { id: string; cover: string | null }
type Pub = { status: number; headers: Headers; text: string; bytes: Uint8Array }

// Bare fetch with no Authorization: the spec reads status, headers and bytes.
async function pub(path: string, init?: { method?: string; headers?: Record<string, string> }): Promise<Pub> {
  const res = await fetch(`${apiBase()}${path}`, init)
  const bytes = new Uint8Array(await res.arrayBuffer())
  return { status: res.status, headers: res.headers, text: new TextDecoder().decode(bytes), bytes }
}

const categoryInput = (name: string) => ({ name, headline: '', intro: '', icon: 'package', tone: 0 })

test.describe.configure({ mode: 'serial' })

test.describe('public blog (API E2E, over the deployed gateway)', () => {
  let staff: string
  let account: StaffAccount
  let tag: string
  let catA: Category
  let catB: Category
  const postIds: string[] = []

  const send = (method: string, path: string, body?: unknown) =>
    rawFetch(`${STAFF}${path}`, { method, headers: { Authorization: `Bearer ${staff}` }, body })

  async function putCover(id: string, bytes: Uint8Array<ArrayBuffer>): Promise<void> {
    const res = await fetch(`${apiBase()}${STAFF}/posts/${id}/cover`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${staff}` },
      body: bytes,
    })
    expect(res.status, `PUT cover on ${id}`).toBe(200)
  }

  // Creates a draft and, when date is given, publishes it with that date. Returns the post id (the slug).
  async function makePost(category: Category, name: string, date?: string): Promise<string> {
    const title = `e2e public ${tag} ${name}`
    const created = await send('POST', '/posts', { title, category_id: category.id, status: 'draft', body: [] })
    expect(created.status, `create ${name}`).toBe(201)
    const id = (created.body as { id: string }).id
    postIds.push(id)
    if (date) await setStatus(id, category, name, 'published', date)
    return id
  }

  async function setStatus(id: string, category: Category, name: string, status: 'published' | 'draft', date: string): Promise<void> {
    const res = await send('PUT', `/posts/${id}`, {
      title: `e2e public ${tag} ${name}`,
      summary: `Summary ${name}.`,
      category_id: category.id,
      publish_date: date,
      author_id: account.userId,
      status,
      body: [{ type: 'p', text: `Body ${name}.` }],
    })
    expect(res.status, `PUT ${name} as ${status}`).toBe(200)
  }

  const listed = async (): Promise<PublicPost[]> => {
    const res = await pub('/blog/posts')
    expect(res.status, 'GET /blog/posts').toBe(200)
    const posts = (JSON.parse(res.text) as { posts: PublicPost[] }).posts
    expect(posts.length, 'the list is not empty, so an absence check can fail').toBeGreaterThan(0)
    return posts
  }

  test.beforeAll(async () => {
    tag = crypto.randomUUID().replace(/-/g, '').slice(0, 10)
    account = await provisionStaffAccount('public-blog')
    staff = (await signInSession(account.email, account.password)).access_token
    for (const [n, set] of [['a', (c: Category) => (catA = c)], ['b', (c: Category) => (catB = c)]] as const) {
      const res = await send('POST', '/categories', categoryInput(`e2e public ${tag} ${n}`))
      expect(res.status, `add category ${n}`).toBe(201)
      set(res.body as Category)
    }
  })

  test.afterAll(async () => {
    if (!staff) return
    for (const id of postIds) await send('DELETE', `/posts/${id}`)
    for (const cat of [catA, catB]) if (cat) await send('DELETE', `/categories/${cat.id}`)
  })

  test('public blog: categories and posts answer anonymously with the short cache', async () => {
    const cats = await pub('/blog/categories')
    expect(cats.status, 'GET /blog/categories').toBe(200)
    expect(cats.headers.get('cache-control')).toBe(SHORT_CACHE)
    const categories = (JSON.parse(cats.text) as { categories: Category[] }).categories
    const posts = await pub('/blog/posts')
    expect(posts.status, 'GET /blog/posts').toBe(200)
    expect(posts.headers.get('cache-control')).toBe(SHORT_CACHE)
    const list = (JSON.parse(posts.text) as { posts: (PublicPost & { category_id: string })[] }).posts
    const byId = new Map(list.map((p) => [p.id, p]))
    for (const id of SEEDED_POST_IDS) {
      expect(byId.has(id), `seeded post ${id} is listed`).toBe(true)
      expect(categories.map((c) => c.id), `category of ${id}`).toContain(byId.get(id)!.category_id)
    }
  })

  test('public blog: draft, future and unknown slugs are the same 404', async () => {
    const draft = await makePost(catA, 'draft')
    const future = await makePost(catA, 'future', HIDDEN_DATE)
    const unknown = `e2e-no-such-post-${tag}`
    const answers = [] as Pub[]
    for (const slug of [draft, future, unknown]) {
      const res = await pub(`/blog/posts/${slug}`)
      expect(res.status, `GET ${slug}`).toBe(404)
      expect(res.headers.get('cache-control'), `cache of ${slug}`).toBe(SHORT_CACHE)
      answers.push(res)
    }
    expect(answers[0].text, 'the 404 carries a body').toContain('not found')
    expect(answers[1].text, 'future matches draft').toBe(answers[0].text)
    expect(answers[2].text, 'unknown matches draft').toBe(answers[0].text)
    const ids = (await listed()).map((p) => p.id)
    expect(ids).not.toContain(draft)
    expect(ids).not.toContain(future)
  })

  test('public blog: a post dated yesterday in Lagos is readable', async () => {
    const id = await makePost(catA, 'visible', VISIBLE_DATE)
    const res = await pub(`/blog/posts/${id}`)
    expect(res.status, 'GET by slug').toBe(200)
    const post = JSON.parse(res.text) as Record<string, unknown>
    expect(post).toMatchObject({
      id,
      category_id: catA.id,
      publish_date: VISIBLE_DATE,
      title: `e2e public ${tag} visible`,
      summary: 'Summary visible.',
      author_name: STAFF_DISPLAY_NAME,
      featured: false,
    })
    expect(post.body, 'the body is present').toEqual([{ type: 'p', text: 'Body visible.' }])
    expect(post).not.toHaveProperty('status')
    expect(post).not.toHaveProperty('author_id')
    const inList = (await listed()).find((p) => p.id === id)
    expect(inList, 'listed').toBeDefined()
    expect(inList).not.toHaveProperty('status')
    expect(inList).not.toHaveProperty('author_id')
    const cats = JSON.parse((await pub('/blog/categories')).text) as { categories: Category[] }
    expect(cats.categories.find((c) => c.id === catA.id)?.post_count, 'the draft and future posts do not count').toBe(1)
  })

  test('public blog: a cover is immutable at its version and moves on replace', async () => {
    const id = await makePost(catB, 'cover', VISIBLE_DATE)
    await putCover(id, PNG_1X1)
    const first = (await listed()).find((p) => p.id === id)?.cover
    expect(first, 'the list carries a cover path').toContain(`/blog/covers/${id}/`)
    const a = await pub(first!)
    expect(a.status, 'GET cover').toBe(200)
    expect(a.headers.get('content-type')).toBe('image/png')
    expect(a.headers.get('cache-control')).toBe(COVER_CACHE)
    expect(Buffer.from(a.bytes).equals(Buffer.from(PNG_1X1)), 'uploaded bytes').toBe(true)

    await putCover(id, PNG_1X1_B)
    const second = (await listed()).find((p) => p.id === id)?.cover
    expect(second, 'a new path after the replace').toContain(`/blog/covers/${id}/`)
    expect(second).not.toBe(first)
    const b = await pub(second!)
    expect(b.status, 'GET new cover').toBe(200)
    expect(Buffer.from(b.bytes).equals(Buffer.from(PNG_1X1_B)), 'replaced bytes').toBe(true)
    expect((await pub(first!)).status, 'the old path').toBe(404)
  })

  test('public blog: the blog origin gets the CORS grant', async () => {
    const origin = new URL(resolveTarget('BLOG_URL')).origin
    const get = await pub('/blog/posts', { headers: { Origin: origin } })
    expect(get.status).toBe(200)
    expect(get.headers.get('access-control-allow-origin')).toBe(origin)
    expect(get.headers.get('vary') ?? '').toContain('Origin')
    const pre = await pub('/blog/posts', {
      method: 'OPTIONS',
      headers: { Origin: origin, 'Access-Control-Request-Method': 'GET' },
    })
    expect(pre.status, 'preflight').toBe(204)
    expect(pre.headers.get('access-control-allow-origin')).toBe(origin)
    const other = await pub('/blog/posts', { headers: { Origin: 'https://not-an-origin.example' } })
    expect(other.status, 'another origin still reads').toBe(200)
    expect(other.headers.get('access-control-allow-origin'), 'another origin').toBeNull()
    const otherPre = await pub('/blog/posts', {
      method: 'OPTIONS',
      headers: { Origin: 'https://not-an-origin.example', 'Access-Control-Request-Method': 'GET' },
    })
    expect(otherPre.headers.get('access-control-allow-origin'), 'another origin preflight').toBeNull()
  })

  test('public blog: a publish shows and an unpublish hides on the next read', async () => {
    const id = await makePost(catB, 'toggle')
    expect((await pub(`/blog/posts/${id}`)).status, 'a draft is hidden').toBe(404)
    await setStatus(id, catB, 'toggle', 'published', VISIBLE_DATE)
    expect((await listed()).map((p) => p.id), 'listed after publish').toContain(id)
    expect((await pub(`/blog/posts/${id}`)).status, 'readable after publish').toBe(200)
    await setStatus(id, catB, 'toggle', 'draft', VISIBLE_DATE)
    expect((await pub(`/blog/posts/${id}`)).status, 'hidden after unpublish').toBe(404)
    expect((await listed()).map((p) => p.id), 'unlisted after unpublish').not.toContain(id)
  })

  test('public blog: /api/ stays closed to anonymous callers', async () => {
    expect((await pub('/api/notifications/v1/ping')).status).toBe(401)
    // The verifier runs before the router: anonymous gets 401 on internal; the 404 needs a valid token.
    expect((await pub('/api/notifications/internal/blog/posts')).status, 'anonymous internal').toBe(401)
    const withToken = await rawFetch('/api/notifications/internal/blog/posts', { headers: { Authorization: `Bearer ${staff}` } })
    expect(withToken.status, 'internal with a staff token').toBe(404)
  })
})
