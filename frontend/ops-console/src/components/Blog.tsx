import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchAuthors, fetchCategories, fetchPosts, setFeatured, type Post } from '../blogApi'
import { loadBlog, pinThenRefresh } from '../blogFlow'
import { expireOnce, withSession } from '../session'
import type { BlogFilters, ToastTone } from '../types'
import { BlogList, type BlogState } from './BlogList'

type Props = {
  filters: BlogFilters
  onFilters: (f: BlogFilters) => void
  onToast: (msg: string, tag?: string, tone?: ToastTone) => void
}

const STAFF_ONLY = 'Your account is not ASComply staff.'

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
    const next = await loadBlog({
      fetchPosts: () => withSession(fetchPosts),
      fetchCategories: () => withSession(fetchCategories),
      fetchAuthors: () => withSession(fetchAuthors),
      expire: expireOnce,
    })
    if (alive.current) setState(next)
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  const pinning = useRef(false)
  const onPin = async (post: Post) => {
    if (pinning.current) return
    pinning.current = true
    try {
      const { outcome, after } = await pinThenRefresh(
        { setFeatured: (id, f) => withSession(() => setFeatured(id, f)), fetchPosts: () => withSession(fetchPosts), expire: expireOnce },
        post,
      )
      if (!alive.current) return
      if (outcome.kind === 'done') onToast(outcome.toast, 'BLOG')
      else if (outcome.kind !== 'expired') onToast(outcome.kind === 'forbidden' ? STAFF_ONLY : outcome.message, 'BLOG', 'red')
      if (after.kind === 'posts') setState((s) => (s.status === 'ready' ? { ...s, posts: after.posts } : s))
      else if (after.kind === 'state') setState(after.state)
    } finally {
      pinning.current = false
    }
  }

  return <BlogList state={state} filters={filters} onFilters={onFilters} onRetry={() => void load()} onPin={(p) => void onPin(p)} />
}
