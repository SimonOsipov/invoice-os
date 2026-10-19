// The blog's server-rendered HTML as a crawler reads it, over the deployed blog origin.
// Staff setup goes through the gateway staff routes; the spec removes what it creates.
import { test, expect } from '@playwright/test'
import { provisionStaffAccount, rawFetch, signInSession, apiBase } from './client'
import type { StaffAccount } from './client'
import { resolveTarget } from '../targets'

const STAFF = '/api/notifications/v1/staff/blog'
const GPTBOT = 'Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)'
const BLOG_URL = resolveTarget('BLOG_URL')
const GATEWAY_URL = apiBase()
// siteSuffix and homeHeading in internal/blogweb/render.go.
const SITE_SUFFIX = ' · ASComply blog'
const HOME_TITLE = 'News from the invoice compliance desk'
const STAFF_DISPLAY_NAME = 'ASComply staff'
const POLL_INTERVAL_MS = 2000
const SNAPSHOT_POLL_MS = 75_000 // the snapshot is at most 60 s old
const VISIBILITY_LIMIT_MS = 70_000 // 60 s cache plus poll and network slack

const PNG_1X1 = Uint8Array.from(
  Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64'),
)

// Lagos is UTC+1: yesterday's Lagos date is never today's, so no case sits on the midnight boundary.
const VISIBLE_DATE = new Date(Date.now() + 3600e3 - 86400e3).toISOString().slice(0, 10)
const HIDDEN_DATE = '3001-01-01'

type Category = { id: string; name: string }
type Page = { status: number; type: string; text: string }

const categoryInput = (name: string, intro: string) => ({ name, headline: '', intro, icon: 'package', tone: 0 })

async function crawl(path: string): Promise<Page> {
  const res = await fetch(`${BLOG_URL}${path}`, { headers: { 'User-Agent': GPTBOT }, redirect: 'manual' })
  return { status: res.status, type: res.headers.get('content-type') ?? '', text: await res.text() }
}

// First match of a regex scoped to one tag; the capture is the tag's content.
const first = (text: string, re: RegExp): string | undefined => re.exec(text)?.[1]
const titleOf = (t: string) => first(t, /<title>([^<]*)<\/title>/)
const metaOf = (t: string, name: string) => first(t, new RegExp(`<meta name="${name}" content="([^"]*)">`))
const ogOf = (t: string, prop: string) => first(t, new RegExp(`<meta property="og:${prop}" content="([^"]*)">`))
const canonicalOf = (t: string) => first(t, /<link rel="canonical" href="([^"]*)">/)
const locsOf = (xml: string) => [...xml.matchAll(/<loc>([^<]*)<\/loc>/g)].map((m) => m[1])

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// Polls until ready() holds or the deadline since `since` passes, then fails with the label.
async function pollUntil(label: string, since: number, limitMs: number, ready: () => Promise<boolean>): Promise<void> {
  for (;;) {
    if (await ready()) return
    if (Date.now() - since > limitMs) throw new Error(`${label}: not true within ${limitMs} ms`)
    await sleep(POLL_INTERVAL_MS)
  }
}

test.describe.configure({ mode: 'serial' })

test.describe('blog crawler HTML (API E2E, over the deployed blog origin)', () => {
  let staff: string
  let account: StaffAccount
  let tag: string
  let category: Category
  let visible: string
  let draft: string
  let future: string
  const postIds: string[] = []

  const titleFor = (name: string) => (name === 'visible' ? `Crawler ${tag} <script>alert(1)</script> & co` : `Crawler ${tag} ${name}`)
  const summaryFor = (name: string) => `Crawler summary ${name} ${tag}`
  const marker = (kind: string) => `crawlmark-${kind}-${tag}`
  const body = (name: string) =>
    name === 'visible'
      ? [
          { type: 'p', text: `Paragraph ${marker('p')}.` },
          { type: 'h', text: `Heading ${marker('h')}` },
          { type: 'ul', items: [`Item ${marker('ul')}`, 'Second item'] },
          { type: 'note', kind: 'deadline', title: 'Note title', text: `Note ${marker('note')}` },
          { type: 'quote', text: `Quote ${marker('quote')}`, who: 'A. Speaker' },
        ]
      : [{ type: 'p', text: `Body ${name}.` }]

  const send = (method: string, path: string, payload?: unknown) =>
    rawFetch(`${STAFF}${path}`, { method, headers: { Authorization: `Bearer ${staff}` }, body: payload })

  async function putCover(id: string): Promise<void> {
    const res = await fetch(`${apiBase()}${STAFF}/posts/${id}/cover`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${staff}` },
      body: PNG_1X1,
    })
    expect(res.status, `PUT cover on ${id}`).toBe(200)
  }

  async function setStatus(id: string, name: string, status: 'published' | 'draft', date: string): Promise<void> {
    const res = await send('PUT', `/posts/${id}`, {
      title: titleFor(name),
      summary: summaryFor(name),
      category_id: category.id,
      publish_date: date,
      author_id: account.userId,
      status,
      body: body(name),
    })
    expect(res.status, `PUT ${name} as ${status}: ${JSON.stringify(res.body)}`).toBe(200)
  }

  // Creates a draft and, when date is given, publishes it with that date. Returns the post id (the slug).
  async function makePost(name: string, date?: string): Promise<string> {
    const created = await send('POST', '/posts', { title: titleFor(name), category_id: category.id, status: 'draft', body: [] })
    expect(created.status, `create ${name}: ${JSON.stringify(created.body)}`).toBe(201)
    const id = (created.body as { id: string }).id
    postIds.push(id)
    if (date) await setStatus(id, name, 'published', date)
    else await setStatus(id, name, 'draft', VISIBLE_DATE)
    return id
  }

  test.beforeAll(async () => {
    test.setTimeout(240_000)
    tag = crypto.randomUUID().replace(/-/g, '').slice(0, 10)
    account = await provisionStaffAccount('blog-crawler')
    staff = (await signInSession(account.email, account.password)).access_token
    const res = await send('POST', '/categories', categoryInput(`Crawler cat ${tag}`, `Crawler intro ${tag}`))
    expect(res.status, 'add category').toBe(201)
    category = res.body as Category
    visible = await makePost('visible', VISIBLE_DATE)
    await putCover(visible)
    draft = await makePost('draft')
    future = await makePost('future', HIDDEN_DATE)
    const since = Date.now()
    await pollUntil('the visible post answers 200', since, SNAPSHOT_POLL_MS, async () => (await crawl(`/${visible}`)).status === 200)
    // The cover is part of the same snapshot only once it has been read after the PUT.
    await pollUntil('the visible post carries its cover', since, SNAPSHOT_POLL_MS, async () =>
      ogOf((await crawl(`/${visible}`)).text, 'image') !== undefined)
  })

  test.afterAll(async () => {
    if (!staff) return
    for (const id of postIds) await send('DELETE', `/posts/${id}`)
    if (category) await send('DELETE', `/categories/${category.id}`)
  })

  test('crawler: a post page carries its title, meta, canonical, Open Graph and full body', async () => {
    const res = await crawl(`/${visible}`)
    expect(res.status).toBe(200)
    expect(res.type).toContain('text/html')
    const title = titleOf(res.text)
    expect(title, '<title>').toBeDefined()
    expect(title!.endsWith(SITE_SUFFIX), `title ${title}`).toBe(true)
    expect(metaOf(res.text, 'description')).toBe(summaryFor('visible'))
    expect(canonicalOf(res.text)).toBe(`${BLOG_URL}/${visible}`)
    expect(ogOf(res.text, 'type')).toBe('article')
    expect(ogOf(res.text, 'title'), 'og:title').toBeDefined()
    expect(ogOf(res.text, 'description')).toBe(summaryFor('visible'))
    expect(ogOf(res.text, 'image')?.startsWith(`${GATEWAY_URL}/blog/covers/${visible}/`), `og:image ${ogOf(res.text, 'image')}`).toBe(true)
    expect(res.text.match(/<h1[\s>]/g)?.length, 'one h1').toBe(1)
    expect(res.text).toContain(STAFF_DISPLAY_NAME)
    expect(res.text).toContain(`datetime="${VISIBLE_DATE}"`)
    const offsets = ['p', 'h', 'ul', 'note', 'quote'].map((k) => res.text.indexOf(marker(k)))
    expect(offsets.every((o) => o >= 0), `every marker is present: ${offsets}`).toBe(true)
    expect(offsets, 'markers follow the block order').toEqual([...offsets].sort((a, b) => a - b))
  })

  test('crawler: a script in a title renders as text', async () => {
    const res = await crawl(`/${visible}`)
    const escaped = '&lt;script&gt;alert(1)&lt;/script&gt; &amp; co'
    expect(titleOf(res.text), '<title>').toContain(escaped)
    expect(first(res.text, /<h1[^>]*>([^<]*)<\/h1>/), '<h1>').toContain(escaped)
    expect(res.text.split('<script>alert(1)').length - 1, 'a raw script tag').toBe(0)
  })

  test('crawler: home and category pages list their visible posts', async () => {
    const home = await crawl('/')
    expect(home.status).toBe(200)
    expect(titleOf(home.text)).toBe(`${HOME_TITLE}${SITE_SUFFIX}`)
    expect(metaOf(home.text, 'description'), 'home description').toBeTruthy()
    expect(home.text).toContain(`href="/${visible}"`)
    expect(home.text).toContain(`href="/category/${category.id}"`)
    expect(home.text).not.toContain(`href="/${draft}"`)
    expect(home.text).not.toContain(`href="/${future}"`)

    const cat = await crawl(`/category/${category.id}`)
    expect(cat.status).toBe(200)
    expect(titleOf(cat.text)).toBe(`${category.name}${SITE_SUFFIX}`)
    expect(metaOf(cat.text, 'description')).toBe(`Crawler intro ${tag}`)
    expect(cat.text).toContain(`href="/${visible}"`)
    expect(cat.text).not.toContain(`href="/${draft}"`)
    expect(cat.text).not.toContain(`href="/${future}"`)
  })

  test('crawler: draft, future, unknown slug and unknown category answer 404', async () => {
    for (const path of [`/${draft}`, `/${future}`, `/no-such-post-${tag}`, `/category/no-such-cat-${tag}`]) {
      const res = await crawl(path)
      expect(res.status, `GET ${path}`).toBe(404)
      expect(res.type, `type of ${path}`).toContain('text/html')
      expect(res.text, `noindex on ${path}`).toContain('noindex')
    }
  })

  test('crawler: sitemap.xml lists home, categories with posts and visible posts with lastmod', async () => {
    const res = await crawl('/sitemap.xml')
    expect(res.status).toBe(200)
    expect(res.type).toContain('xml')
    expect(res.text.startsWith('<?xml')).toBe(true)
    expect(res.text).toContain('<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">')
    const locs = locsOf(res.text)
    expect(locs).toContain(`${BLOG_URL}/`)
    expect(locs).toContain(`${BLOG_URL}/category/${category.id}`)
    expect(res.text).toContain(`<loc>${BLOG_URL}/${visible}</loc><lastmod>${VISIBLE_DATE}</lastmod>`)
    expect(locs.filter((l) => !l.startsWith(BLOG_URL)), 'locs outside the blog origin').toEqual([])
    expect(locs).not.toContain(`${BLOG_URL}/${draft}`)
    expect(locs).not.toContain(`${BLOG_URL}/${future}`)
  })

  test('crawler: the sitemap has unique locs, dated entries, and its own pages answer 200', async () => {
    const xml = (await crawl('/sitemap.xml')).text
    const locs = locsOf(xml)
    expect(locs.length, 'sitemap locs').toBeGreaterThanOrEqual(3)
    expect(new Set(locs).size, 'duplicate locs').toBe(locs.length)
    expect(xml.match(/<url>/g)?.length, '<url> count').toBe(locs.length)
    const dates = [...xml.matchAll(/<lastmod>([^<]*)<\/lastmod>/g)].map((m) => m[1])
    expect(dates.length, 'lastmod entries').toBeGreaterThanOrEqual(3)
    expect(dates.filter((d) => !/^\d{4}-\d{2}-\d{2}$/.test(d)), 'malformed lastmod').toEqual([])
    for (const loc of [`${BLOG_URL}/`, `${BLOG_URL}/category/${category.id}`, `${BLOG_URL}/${visible}`]) {
      expect(locs, loc).toContain(loc)
      expect((await crawl(loc.slice(BLOG_URL.length))).status, `GET ${loc}`).toBe(200)
    }
  })

  test('crawler: robots.txt allows crawling and names the sitemap', async () => {
    const res = await crawl('/robots.txt')
    expect(res.status).toBe(200)
    expect(res.type.startsWith('text/plain')).toBe(true)
    const lines = res.text.split('\n')
    expect(lines).toContain('User-agent: *')
    expect(lines).toContain('Allow: /')
    expect(lines).toContain(`Sitemap: ${BLOG_URL}/sitemap.xml`)
    expect(res.text).not.toContain('<html')
  })

  test('crawler: a publish shows and an unpublish hides within the cache time', async () => {
    test.setTimeout(240_000)
    const inSitemap = async () => locsOf((await crawl('/sitemap.xml')).text).includes(`${BLOG_URL}/${draft}`)

    await setStatus(draft, 'draft', 'published', VISIBLE_DATE)
    const t0 = Date.now()
    await pollUntil('the published post answers 200', t0, VISIBILITY_LIMIT_MS, async () => (await crawl(`/${draft}`)).status === 200)
    await pollUntil('the sitemap holds the published post', t0, VISIBILITY_LIMIT_MS, inSitemap)

    await setStatus(draft, 'draft', 'draft', VISIBLE_DATE)
    const t1 = Date.now()
    await pollUntil('the unpublished post answers 404', t1, VISIBILITY_LIMIT_MS, async () => (await crawl(`/${draft}`)).status === 404)
    await pollUntil('the sitemap drops the unpublished post', t1, VISIBILITY_LIMIT_MS, async () => !(await inSitemap()))
  })
})
