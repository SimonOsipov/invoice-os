import { decodeJwtPayload, loadConsoleSession } from '@invoice-os/console-session'
import { useCallback, useEffect, useRef, useState } from 'react'
import { SESSION_KEY } from '../auth'
import { draftFromPost, editorOf, newDraft, setField, subOf, today, type Draft, type EditorState } from '../blog'
import { fetchAuthors, fetchCategories, fetchCoverUrl, fetchPosts, setFeatured, type Post } from '../blogApi'
import { loadBlog, pickCover, pinThenRefresh, trackCover } from '../blogFlow'
import { browserCoverDeps } from '../coverImage'
import { expireOnce, withSession } from '../session'
import type { BlogFilters, ToastTone } from '../types'
import { BlogEditor } from './BlogEditor'
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

  const [editor, setEditor] = useState<EditorState | null>(null)
  const [coverUrl, setCoverUrl] = useState<string | null>(null)
  const openSeq = useRef(0)
  const open = (draft: Draft) => {
    openSeq.current++
    setEditor(editorOf(draft))
  }
  const onNew = () => {
    if (state.status !== 'ready') return
    open(newDraft(state.cats, state.authors, subOf(decodeJwtPayload(loadConsoleSession(SESSION_KEY)?.token ?? null)), today()))
  }
  const onBack = () => {
    openSeq.current++
    setEditor(null)
  }

  const editing = editor !== null
  useEffect(() => {
    if (editing) document.querySelectorAll('[data-bscroll]').forEach((el) => (el.scrollTop = 0))
  }, [editing])

  const savedId = editor?.draft.cover.kind === 'saved' ? editor.draft.id : null
  useEffect(() => {
    setCoverUrl(null)
    return savedId ? trackCover({ fetchCoverUrl: (id) => withSession(() => fetchCoverUrl(id)), expire: expireOnce }, savedId, setCoverUrl) : undefined
  }, [savedId])

  const onCover = async (file: File | null) => {
    const seq = openSeq.current
    const pick = await pickCover(browserCoverDeps, file)
    if (!alive.current || seq !== openSeq.current) return
    if (pick.kind === 'rejected') onToast(pick.toast, 'BLOG', 'red')
    else if (pick.kind === 'ready') setEditor((e) => (e ? setField(e, { cover: { kind: 'new', dataUrl: pick.dataUrl } }) : e))
  }

  if (editor && state.status === 'ready') {
    return (
      <BlogEditor
        editor={editor}
        cats={state.cats}
        authors={state.authors}
        coverUrl={coverUrl}
        onField={(patch) => setEditor((e) => (e ? setField(e, patch) : e))}
        onCover={(f) => void onCover(f)}
        onBack={onBack}
      />
    )
  }

  return <BlogList state={state} filters={filters} onFilters={onFilters} onRetry={() => void load()} onPin={(p) => void onPin(p)} onNew={onNew} onOpen={(p) => open(draftFromPost(p))} />
}
