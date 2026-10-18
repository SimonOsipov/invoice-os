import { useCallback, useEffect, useRef, useState } from 'react'
import { addBlock, askLeave, catFormFrom, coverCss, deleteView, draftFromPost, editorOf, moveBlock, newCatForm, newDraft, patchBlock, previewModel, removeBlock, setCatField, setConfirm, setField, setPreview, tabChange, takeKept, today, writeKept, type BlockType, type CatUi, type Draft, type EditorState } from '../blog'
import { addCategory, createPost, deleteCategory, deletePost, fetchAuthors, fetchCategories, fetchCoverUrl, fetchPosts, moveCategory, putCover, removeCover, setFeatured, updateCategory, updatePost, type Post } from '../blogApi'
import { applyCatMove, applyCatRefresh, applyCatRemove, applyCatSave, applyRemove, applySave, keepOnExpire, keepThenExpire, loadBlog, moveCategoryFlow, pickCover, pinThenRefresh, postsAfterRemove, refreshCategories, removeCategory, removePost, saveCategory, savePost, singleFlight, STAFF_ONLY, trackCover } from '../blogFlow'
import { browserCoverDeps } from '../coverImage'
import { currentSub, expireOnce, withSession } from '../session'
import type { BlogFilters, ToastTone } from '../types'
import { BlogCategories } from './BlogCategories'
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

  const [catUi, setCatUi] = useState<CatUi>({ form: null, del: null })
  const applyCats = (a: ReturnType<typeof applyCatMove>) => {
    const { cats } = a
    if (cats) setState((s) => (s.status === 'ready' ? { ...s, cats } : s))
    if (a.toast) onToast(a.toast, 'BLOG', a.tone)
    if (a.expire) expireOnce()
  }
  const reloadCats = (queue = false) =>
    (queue ? flight.after : flight)(async () => {
      const a = applyCatRefresh(await refreshCategories({ fetchCategories: () => withSession(fetchCategories) }))
      if (alive.current) applyCats(a)
    })
  const onMoveCat = async (id: string, direction: 'up' | 'down') => {
    let reread = false
    await flight(async () => {
      const a = applyCatMove(await moveCategoryFlow({ moveCategory: (i, d) => withSession(() => moveCategory(i, d)) }, id, direction))
      if (!alive.current) return
      applyCats(a)
      reread = a.reread
    })
    if (reread && alive.current) await reloadCats()
  }
  const catUiRef = useRef(catUi)
  catUiRef.current = catUi
  const filtersRef = useRef(filters)
  filtersRef.current = filters
  const stateRef = useRef(state)
  stateRef.current = state
  const settle = async (reread: boolean) => {
    if (reread && alive.current) await reloadCats()
  }
  const onCatSave = async () => {
    let reread = false
    await flight(async () => {
      const form = catUiRef.current.form
      if (!form) return
      const o = await saveCategory(
        { addCategory: (i) => withSession(() => addCategory(i)), updateCategory: (id, i) => withSession(() => updateCategory(id, i)), fetchCategories: () => withSession(fetchCategories) },
        form,
      )
      if (!alive.current) return
      const cur = stateRef.current
      const a = applyCatSave(catUiRef.current, o, cur.status === 'ready' ? cur.cats : [])
      setCatUi(a.ui)
      applyCats(a)
      reread = a.reread
    })
    await settle(reread)
  }
  const onCatDelete = async () => {
    let reread = false
    await flight(async () => {
      const del = catUiRef.current.del
      const s = stateRef.current
      if (!del || s.status !== 'ready') return
      const o = await removeCategory(
        { deleteCategory: (id, to) => withSession(() => deleteCategory(id, to)), fetchCategories: () => withSession(fetchCategories), fetchPosts: () => withSession(fetchPosts) },
        s.cats,
        del.id,
        del.moveTo,
      )
      const cur = stateRef.current
      if (!alive.current || cur.status !== 'ready') return
      const a = applyCatRemove({ cats: cur.cats, posts: cur.posts }, catUiRef.current, filtersRef.current, del.id, o)
      setCatUi(a.ui)
      const { cats, posts } = a
      setState((x) => (x.status === 'ready' ? { ...x, cats, posts } : x))
      if (a.filters !== filtersRef.current) onFilters(a.filters)
      if (a.toast) onToast(a.toast, 'BLOG', a.tone)
      if (a.expire) expireOnce()
      reread = a.reread
    })
    await settle(reread)
  }
  const onTabFilters = (f: BlogFilters) => {
    const t = tabChange(filters.tab, f.tab, catUi)
    setCatUi(t.ui)
    onFilters(f)
    if (t.refresh) void reloadCats(true)
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
    open(newDraft(state.cats, state.authors, currentSub(), today()))
  }
  const close = () => {
    openSeq.current++
    setEditor(null)
  }
  const onBack = () => (editor?.dirty ? setEditor(askLeave(editor)) : close())

  const editorRef = useRef(editor)
  editorRef.current = editor
  const keepDeps = {
    write: (d: Draft) => {
      const owner = currentSub()
      if (owner) writeKept(localStorage, owner, d)
    },
    expire: expireOnce,
  }
  const keep = (e: EditorState | null) => keepThenExpire(keepDeps, e)

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
    return savedId ? trackCover({ fetchCoverUrl: (id) => withSession(() => fetchCoverUrl(id)), expire: keepOnExpire(keepDeps, () => editorRef.current) }, savedId, setCoverUrl) : undefined
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

  const categories =
    state.status === 'ready' ? (
      <BlogCategories
        cats={state.cats}
        onMove={(id, d) => void onMoveCat(id, d)}
        form={catUi.form}
        del={catUi.del}
        onAdd={() => setCatUi({ form: newCatForm(state.cats), del: null })}
        onEdit={(c) => setCatUi({ form: catFormFrom(c), del: null })}
        onDelete={(c) => {
          const v = deleteView(state.cats, c.id)
          setCatUi({ form: null, del: { id: c.id, moveTo: v?.kind === 'withposts' ? v.defaultMoveTo : null } })
        }}
        onField={(patch) => setCatUi((u) => (u.form ? { ...u, form: setCatField(u.form, patch) } : u))}
        onSubmit={() => void onCatSave()}
        onCancelForm={() => setCatUi((u) => ({ ...u, form: null }))}
        onMoveTo={(id) => setCatUi((u) => (u.del ? { ...u, del: { ...u.del, moveTo: id } } : u))}
        onCancelDel={() => setCatUi((u) => ({ ...u, del: null }))}
        onConfirmDel={() => void onCatDelete()}
      />
    ) : null

  return <BlogList state={state} filters={filters} onFilters={onTabFilters} categories={categories} onRetry={() => void load()} onPin={(p) => void onPin(p)} onNew={onNew} onOpen={(p) => open(draftFromPost(p))} />
}
