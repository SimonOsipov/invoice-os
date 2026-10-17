import { request } from './api'

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
  title: string
  [extra: string]: unknown
}

export interface Cover {
  content_type: string
  size_bytes: number
  updated_at: string
}

export interface Post extends PostInput {
  id: string
  cover: Cover | null
}

export interface Category {
  id: string
  name: string
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

export const fetchPosts = (): Promise<Post[]> => request<Post[]>(`${BLOG}/stub`)
export const fetchCategories = (): Promise<Category[]> => request<Category[]>(`${BLOG}/stub`)
export const fetchAuthors = (): Promise<Author[]> => request<Author[]>(`${BLOG}/stub`)
export const createPost = (_input: PostInput): Promise<Post> => request<Post>(`${BLOG}/stub`)
export const updatePost = (_id: string, _input: PostInput): Promise<Post> => request<Post>(`${BLOG}/stub`)
export const deletePost = (_id: string): Promise<void> => request<void>(`${BLOG}/stub`)
export const setFeatured = (_id: string, _featured: boolean): Promise<FeaturedResult> => request<FeaturedResult>(`${BLOG}/stub`)
export const removeCover = (_id: string): Promise<void> => request<void>(`${BLOG}/stub`)
export const putCover = (_id: string, _bytes: Uint8Array): Promise<Cover> => Promise.resolve(undefined as unknown as Cover)
export const fetchCoverUrl = (_id: string): Promise<{ url: string; release: () => void }> => Promise.resolve({ url: '', release: () => undefined })
export const fieldErrorsOf = (_err: unknown): { summary: string; fields: Record<string, string> } | null => null
