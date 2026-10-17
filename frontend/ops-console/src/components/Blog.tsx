import { ApiError } from '@invoice-os/api-client'
import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchAuthors, fetchCategories, fetchPosts, setFeatured, type Post } from '../blogApi'
import { pinPost } from '../blogFlow'
import { expireOnce, withSession } from '../session'
import type { BlogFilters, ToastTone } from '../types'
import { BlogList, type BlogState } from './BlogList'

type Props = {
  filters: BlogFilters
  onFilters: (f: BlogFilters) => void
  onToast: (msg: string, tag?: string, tone?: ToastTone) => void
}

const STAFF_ONLY = 'Your account is not ASComply staff.'

function failure(e: unknown): BlogState {
  if (e instanceof ApiError && e.status === 401) expireOnce()
  if (e instanceof ApiError && e.status === 403) return { status: 'forbidden' }
  return { status: 'error', message: e instanceof Error ? e.message : 'Could not load posts' }
}

export function Blog({ filters, onFilters, onToast }: Props) {
  const [state, setState] = useState<BlogState>({ status: 'loading' })
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  const load = useCallback(async () => {
    setState({ status: 'loading' })
    try {
      const [posts, cats, authors] = await Promise.all([
        withSession(fetchPosts),
        withSession(fetchCategories),
        withSession(fetchAuthors),
      ])
      if (alive.current) setState({ status: 'ready', posts, cats, authors })
    } catch (e) {
      if (alive.current) setState(failure(e))
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  const pinning = useRef(false)
  const onPin = async (post: Post) => {
    if (pinning.current) return
    pinning.current = true
    try {
      const out = await pinPost({ setFeatured: (id, f) => withSession(() => setFeatured(id, f)) }, post)
      if (!alive.current) return
      if (out.kind === 'expired') return expireOnce()
      if (out.kind === 'done') onToast(out.toast, 'BLOG')
      else onToast(out.kind === 'forbidden' ? STAFF_ONLY : out.message, 'BLOG', 'red')
      try {
        const posts = await withSession(fetchPosts)
        if (alive.current) setState((s) => (s.status === 'ready' ? { ...s, posts } : s))
      } catch (e) {
        if (alive.current) setState(failure(e))
      }
    } finally {
      pinning.current = false
    }
  }

  return <BlogList state={state} filters={filters} onFilters={onFilters} onRetry={() => void load()} onPin={(p) => void onPin(p)} />
}
