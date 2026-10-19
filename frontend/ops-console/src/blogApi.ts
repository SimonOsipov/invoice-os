import { ApiError, gatewayBase, readBody, reportApiFailure } from '@invoice-os/api-client'
import { loadConsoleSession } from '@invoice-os/console-session'
import { request } from './api'
import { SESSION_KEY } from './auth'

// Wire shapes: internal/blog/blog.go (Block, PostInput, Post, Cover, Category, Author), posts.go (FeaturedResult).
export interface Block {
  type: string
  text?: string
  items?: string[]
  kind?: string
  title?: string
  who?: string
  demo?: string
}

export interface PostInput {
  category_id: string | null
  publish_date: string | null
  title: string
  summary: string
  author_id: string | null
  status: 'draft' | 'published'
  featured: boolean
  body: Block[]
}

export interface Cover {
  content_type: string
  size_bytes: number
  updated_at: string
}

export interface Post extends PostInput {
  id: string
  author_name: string | null
  cover: Cover | null
  created_at: string
  updated_at: string
}

export interface Category {
  id: string
  name: string
  headline: string
  intro: string
  icon: string
  tone: number
  post_count: number
}

export interface CategoryInput {
  name: string
  headline: string
  intro: string
  icon: string
  tone: number
}

export interface MoveResult {
  moved: boolean
  categories: Category[]
}

export interface DeleteResult {
  moved_posts: number
  move_to: string | null
}

export interface Author {
  user_id: string
  display_name: string | null
  email: string | null
}

export interface FeaturedResult {
  id: string
  featured: boolean
  unpinned: string | null
}

export const BLOG = '/api/notifications/v1/staff/blog'

const postPath = (id: string) => `${BLOG}/posts/${encodeURIComponent(id)}`
const catPath = (id: string) => `${BLOG}/categories/${encodeURIComponent(id)}`

export const fetchPosts = async (): Promise<Post[]> => (await request<{ posts: Post[] }>(`${BLOG}/posts`)).posts
export const fetchCategories = async (): Promise<Category[]> => (await request<{ categories: Category[] }>(`${BLOG}/categories`)).categories
export const fetchAuthors = async (): Promise<Author[]> => (await request<{ authors: Author[] }>(`${BLOG}/authors`)).authors
export const createPost = (input: PostInput): Promise<Post> => request<Post>(`${BLOG}/posts`, { method: 'POST', body: input })
export const updatePost = (id: string, input: PostInput): Promise<Post> => request<Post>(postPath(id), { method: 'PUT', body: input })
export const deletePost = (id: string): Promise<void> => request<void>(postPath(id), { method: 'DELETE' })
export const setFeatured = (id: string, featured: boolean): Promise<FeaturedResult> =>
  request<FeaturedResult>(`${postPath(id)}/featured`, { method: 'PUT', body: { featured } })
export const removeCover = (id: string): Promise<void> => request<void>(`${postPath(id)}/cover`, { method: 'DELETE' })
export const addCategory = (input: CategoryInput): Promise<Category> => request<Category>(`${BLOG}/categories`, { method: 'POST', body: input })
export const updateCategory = (id: string, input: CategoryInput): Promise<Category> => request<Category>(catPath(id), { method: 'PUT', body: input })
export const moveCategory = (id: string, direction: 'up' | 'down'): Promise<MoveResult> =>
  request<MoveResult>(`${catPath(id)}/move`, { method: 'POST', body: { direction } })
export const deleteCategory = (id: string, moveTo: string | null): Promise<DeleteResult> =>
  request<DeleteResult>(moveTo ? `${catPath(id)}?move_to=${encodeURIComponent(moveTo)}` : catPath(id), { method: 'DELETE' })

const coverUrl = (id: string) => `${gatewayBase()}${postPath(id)}/cover`

// Bare fetch: the cover body is bytes both ways, which `request` would send and parse as JSON.
async function coverFetch(id: string, init: RequestInit): Promise<Response> {
  if (!gatewayBase()) throw new ApiError('network', 'Gateway URL is not configured')
  const token = loadConsoleSession(SESSION_KEY)?.token
  if (!token) throw new ApiError('http', 'Not signed in', 401)
  const method = init.method ?? 'GET'
  const url = coverUrl(id)
  let res: Response
  try {
    res = await fetch(url, { ...init, headers: { ...(init.headers as Record<string, string>), Authorization: `Bearer ${token}` } })
  } catch (e) {
    reportApiFailure(e, { method, url })
    throw e
  }
  if (!res.ok) {
    let body: unknown
    let msg = res.statusText
    try {
      body = await res.json()
      if (body && typeof body === 'object' && 'error' in body) msg = String((body as { error: unknown }).error)
    } catch {
      // no JSON body: the status text stands
    }
    const error = new ApiError('http', msg, res.status, body)
    reportApiFailure(error, { method, url })
    throw error
  }
  return res
}

export async function putCover(id: string, bytes: Uint8Array): Promise<Cover> {
  const res = await coverFetch(id, { method: 'PUT', headers: { 'Content-Type': 'image/jpeg' }, body: bytes as BodyInit })
  return readBody(() => res.json() as Promise<Cover>, { method: 'PUT', url: coverUrl(id) })
}

export async function fetchCoverUrl(id: string): Promise<{ url: string; release: () => void }> {
  const res = await coverFetch(id, {})
  const url = URL.createObjectURL(await readBody(() => res.blob(), { method: 'GET', url: coverUrl(id) }))
  let released = false
  return {
    url,
    release: () => {
      if (released) return
      released = true
      URL.revokeObjectURL(url)
    },
  }
}

export function fieldErrorsOf(err: unknown): { summary: string; fields: Record<string, string> } | null {
  if (!(err instanceof ApiError) || err.status !== 422) return null
  const fields = (err.body as { fields?: unknown } | undefined)?.fields
  if (!fields || typeof fields !== 'object' || Array.isArray(fields)) return null
  return { summary: err.message, fields: fields as Record<string, string> }
}
