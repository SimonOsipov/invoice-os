import { useCallback, useEffect, useRef, useState } from 'react'
import { addBlock, askLeave, coverCss, draftFromPost, editorOf, moveBlock, newDraft, patchBlock, previewModel, removeBlock, setConfirm, setField, setPreview, takeKept, today, writeKept, type BlockType, type Draft, type EditorState } from '../blog'
import { createPost, deletePost, fetchAuthors, fetchCategories, fetchCoverUrl, fetchPosts, putCover, removeCover, setFeatured, updatePost, type Post } from '../blogApi'
import { applyRemove, applySave, keepThenExpire, loadBlog, pickCover, pinThenRefresh, postsAfterRemove, removePost, savePost, singleFlight, STAFF_ONLY, trackCover } from '../blogFlow'
import { browserCoverDeps } from '../coverImage'
import { currentSub, expireOnce, withSession } from '../session'
import type { BlogFilters, ToastTone } from '../types'
import { BlogEditor } from './BlogEditor'
import { BlogList, type BlogState } from './BlogList'
import { BlogPreview } from './BlogPreview'

type Props = {
  filters: BlogFilters
  onFilters: (f: BlogFilters) => void
  onToast: (msg: string, tag?: string, tone?: ToastTone) => void
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

  const flight = useRef(singleFlight()).current
  const onPin = (post: Post) =>
    flight(async () => {
      const { outcome, after } = await pinThenRefresh(
        { setFeatured: (id, f) => withSession(() => setFeatured(id, f)), fetchPosts: () => withSession(fetchPosts), expire: expireOnce },
        post,
      )
      if (!alive.current) return
      if (outcome.kind === 'done') onToast(outcome.toast, 'BLOG')
      else if (outcome.kind !== 'expired') onToast(outcome.kind === 'forbidden' ? STAFF_ONLY : outcome.message, 'BLOG', 'red')
      if (after.kind === 'posts') setState((s) => (s.status === 'ready' ? { ...s, posts: after.posts } : s))
      else if (after.kind === 'state') setState(after.state)
    })

  const [editor, setEditor] = useState<EditorState | null>(null)
  const [coverUrl, setCoverUrl] = useState<string | null>(null)
  const openSeq = useRef(0)
  const open = (draft: Draft) => {
    openSeq.current++
    setEditor(editorOf(draft))
  }
  const onNew = () => {
    if (state.status !== 'ready') return
    open(newDraft(state.cats, state.authors, currentSub(), today()))
  }
  const close = () => {
    openSeq.current++
    setEditor(null)
  }
  const onBack = () => (editor?.dirty ? setEditor(askLeave(editor)) : close())

  const editorRef = useRef(editor)
  editorRef.current = editor
  const keep = (e: EditorState | null) =>
    keepThenExpire(
      {
        write: (d) => {
          const owner = currentSub()
          if (owner) writeKept(localStorage, owner, d)
        },
        expire: expireOnce,
      },
      e,
    )

  const restored = useRef(false)
  useEffect(() => {
    if (restored.current || state.status !== 'ready') return
    restored.current = true
    const owner = currentSub()
    const kept = owner && typeof localStorage !== 'undefined' ? takeKept(localStorage, owner, state.posts) : null
    if (kept) {
      openSeq.current++
      setEditor(kept)
    }
  }, [state])

  const onSave = (status: Draft['status']) =>
    flight(async () => {
      const sent = editorRef.current
      if (!sent) return
      const seq = openSeq.current
      const o = await savePost(
        {
          createPost: (i) => withSession(() => createPost(i)),
          updatePost: (id, i) => withSession(() => updatePost(id, i)),
          putCover: (id, b) => withSession(() => putCover(id, b)),
          removeCover: (id) => withSession(() => removeCover(id)),
          fetchPosts: () => withSession(fetchPosts),
        },
        sent,
        status,
      )
      if (!alive.current) return
      const cur = seq === openSeq.current ? editorRef.current : null
      const a = applySave(cur ?? sent, sent, o)
      if (cur) setEditor(a.editor)
      const { posts } = a
      if (posts) setState((s) => (s.status === 'ready' ? { ...s, posts } : s))
      if (a.toast) onToast(a.toast, 'BLOG', a.tone)
      if (a.expire) keep(cur ? a.editor : null)
    })

  const onDelete = () =>
    flight(async () => {
      const sent = editorRef.current
      if (!sent) return
      const seq = openSeq.current
      const o = await removePost({ deletePost: (id) => withSession(() => deletePost(id)), fetchPosts: () => withSession(fetchPosts) }, sent.draft.id)
      if (!alive.current) return
      const open = seq === openSeq.current
      const a = applyRemove(open ? editorRef.current ?? sent : sent, o)
      setState((s) => (s.status === 'ready' ? { ...s, posts: postsAfterRemove(s.posts, sent.draft.id, o) } : s))
      if (open) {
        if (a.editor) setEditor(a.editor)
        else close()
      }
      if (a.toast) onToast(a.toast, 'BLOG', a.tone)
      if (a.expire) keep(open ? a.editor : null)
    })

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

  const update = (f: (e: EditorState) => EditorState) => setEditor((e) => (e ? f(e) : e))
  const onAdd = (type: BlockType) => {
    update((e) => addBlock(e, type))
    setTimeout(() => {
      const el = document.querySelector('[data-bscroll]')
      if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    }, 30)
  }

  if (editor && state.status === 'ready') {
    return (
      <BlogEditor
        editor={editor}
        cats={state.cats}
        authors={state.authors}
        coverUrl={coverUrl}
        onField={(patch) => update((e) => setField(e, patch))}
        onCover={(f) => void onCover(f)}
        onBack={onBack}
        onSave={(status) => void onSave(status)}
        onAskDelete={() => update((e) => setConfirm(e, 'delete'))}
        onCancelConfirm={() => update((e) => setConfirm(e, null))}
        onDelete={() => void onDelete()}
        onDiscard={close}
        onAdd={onAdd}
        onMove={(i, d) => update((e) => moveBlock(e, i, d))}
        onRemove={(i) => update((e) => removeBlock(e, i))}
        onPatch={(i, patch) => update((e) => patchBlock(e, i, patch))}
        preview={
          <BlogPreview
            model={previewModel(editor.draft, state.cats, state.authors, state.posts.map((p) => p.id))}
            tab={editor.preview}
            cover={coverCss(editor.draft.cover, coverUrl)}
            onTab={(t) => update((e) => setPreview(e, t))}
          />
        }
      />
    )
  }

  return <BlogList state={state} filters={filters} onFilters={onFilters} onRetry={() => void load()} onPin={(p) => void onPin(p)} onNew={onNew} onOpen={(p) => open(draftFromPost(p))} />
}
