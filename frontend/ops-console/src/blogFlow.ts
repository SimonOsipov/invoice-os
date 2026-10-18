import { ApiError } from '@invoice-os/api-client'
import { catDeleteToast, catFormError, catSaveToast, COVER_QUALITY, dataUrlToBytes, errorsFrom422, fitWidth, isImageType, partialEditor, pinToast, saveToast, savedEditor, toCategoryInput, toPostInput, type CatForm, type CatUi, type Draft, type EditorState } from './blog'
import { fieldErrorsOf, type Author, type Category, type CategoryInput, type Cover, type DeleteResult, type FeaturedResult, type MoveResult, type Post, type PostInput } from './blogApi'
import type { BlogState } from './components/BlogList'
import type { BlogFilters, ToastTone } from './types'

export type PinOutcome = { kind: 'done'; toast: string } | { kind: 'expired' } | { kind: 'forbidden' } | { kind: 'failed'; message: string }

export type PinDeps = { setFeatured: (id: string, featured: boolean) => Promise<FeaturedResult> }

export async function pinPost(deps: PinDeps, post: Post): Promise<PinOutcome> {
  try {
    await deps.setFeatured(post.id, !post.featured)
    return { kind: 'done', toast: pinToast(post.featured) }
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return { kind: 'expired' }
    if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not update the post' }
  }
}

export type LoadDeps = {
  fetchPosts: () => Promise<Post[]>
  fetchCategories: () => Promise<Category[]>
  fetchAuthors: () => Promise<Author[]>
  expire: () => void
}

export function readFailure(e: unknown, expire: () => void): BlogState {
  if (e instanceof ApiError && e.status === 401) expire()
  if (e instanceof ApiError && e.status === 403) return { status: 'forbidden' }
  return { status: 'error', message: e instanceof Error ? e.message : 'Could not load posts' }
}

export async function loadBlog(d: LoadDeps): Promise<BlogState> {
  try {
    const [posts, cats, authors] = await Promise.all([d.fetchPosts(), d.fetchCategories(), d.fetchAuthors()])
    return { status: 'ready', posts, cats, authors }
  } catch (e) {
    return readFailure(e, d.expire)
  }
}

export type PinFlowDeps = PinDeps & { fetchPosts: () => Promise<Post[]>; expire: () => void }
export type PinAfter = { kind: 'posts'; posts: Post[] } | { kind: 'state'; state: BlogState } | { kind: 'none' }

export async function pinThenRefresh(d: PinFlowDeps, post: Post): Promise<{ outcome: PinOutcome; after: PinAfter }> {
  const outcome = await pinPost(d, post)
  if (outcome.kind === 'expired') {
    d.expire()
    return { outcome, after: { kind: 'none' } }
  }
  try {
    return { outcome, after: { kind: 'posts', posts: await d.fetchPosts() } }
  } catch (e) {
    return { outcome, after: { kind: 'state', state: readFailure(e, d.expire) } }
  }
}

export const BAD_IMAGE = 'Choose a JPG or PNG image'
export const COVER_TOO_LARGE = 'Choose an image of 2 MB or less'
export const COVER_MAX_BYTES = 2 << 20

export type CoverPickDeps = {
  readAsDataUrl: (file: Blob) => Promise<string>
  loadImage: (src: string) => Promise<{ width: number; height: number }>
  toJpeg: (image: { width: number; height: number }, width: number, height: number, quality: number) => string
}

export type CoverPick = { kind: 'ignore' } | { kind: 'rejected'; toast: string } | { kind: 'ready'; dataUrl: string }

export async function pickCover(d: CoverPickDeps, file: Blob | null | undefined): Promise<CoverPick> {
  if (!file) return { kind: 'ignore' }
  if (!isImageType(file.type)) return { kind: 'rejected', toast: BAD_IMAGE }
  let dataUrl: string
  try {
    const img = await d.loadImage(await d.readAsDataUrl(file))
    if (!(img.width > 0 && img.height > 0)) throw new Error('empty image')
    const { width, height } = fitWidth(img.width, img.height)
    dataUrl = d.toJpeg(img, width, height, COVER_QUALITY)
  } catch {
    return { kind: 'rejected', toast: BAD_IMAGE }
  }
  if (dataUrlToBytes(dataUrl).length > COVER_MAX_BYTES) return { kind: 'rejected', toast: COVER_TOO_LARGE }
  return { kind: 'ready', dataUrl }
}

export type CoverHandle = { url: string; release: () => void }

// Fetches a saved cover; `onUrl(null)` on failure, `expire` on a 401 that survived the renewal. The returned stop releases the URL, also when the fetch lands late.
export function trackCover(d: { fetchCoverUrl: (id: string) => Promise<CoverHandle>; expire: () => void }, id: string, onUrl: (url: string | null) => void): () => void {
  let live = true
  let handle: CoverHandle | null = null
  d.fetchCoverUrl(id).then(
    (h) => {
      if (!live) return h.release()
      handle = h
      onUrl(h.url)
    },
    (e) => {
      if (e instanceof ApiError && e.status === 401) d.expire()
      if (live) onUrl(null)
    },
  )
  return () => {
    live = false
    handle?.release()
    handle = null
  }
}

export const STAFF_ONLY = 'Your account is not ASComply staff.'

// Runs one action at a time; a call made while one runs is ignored.
export function singleFlight() {
  let busy = false
  return async (run: () => Promise<void>): Promise<void> => {
    if (busy) return
    busy = true
    try {
      await run()
    } finally {
      busy = false
    }
  }
}

export type SaveDeps = {
  createPost: (input: PostInput) => Promise<Post>
  updatePost: (id: string, input: PostInput) => Promise<Post>
  putCover: (id: string, bytes: Uint8Array) => Promise<Cover>
  removeCover: (id: string) => Promise<void>
  fetchPosts: () => Promise<Post[]>
}

export type SaveOutcome =
  | { kind: 'saved'; post: Post; toast: string; posts?: Post[]; rereadError?: string }
  | { kind: 'invalid'; errors: Record<string, string>; toast: string }
  | { kind: 'expired'; savedPost?: Post }
  | { kind: 'forbidden'; savedPost?: Post }
  | { kind: 'failed'; message: string; savedPost?: Post }

const failure = (e: unknown, savedPost?: Post): SaveOutcome => {
  if (e instanceof ApiError && e.status === 401) return { kind: 'expired', savedPost }
  if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden', savedPost }
  return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not save the post', savedPost }
}

export async function savePost(deps: SaveDeps, state: EditorState, status: PostInput['status']): Promise<SaveOutcome> {
  const { draft } = state
  const was = draft.id ? draft.status : ''
  const input = toPostInput(draft, status)
  let post: Post
  try {
    post = draft.id ? await deps.updatePost(draft.id, input) : await deps.createPost(input)
  } catch (e) {
    const f = fieldErrorsOf(e)
    return f ? { kind: 'invalid', errors: errorsFrom422(f.fields), toast: f.summary } : failure(e)
  }
  try {
    if (draft.cover.kind === 'new') await deps.putCover(post.id, dataUrlToBytes(draft.cover.dataUrl))
    else if (draft.cover.kind === 'none' && draft.serverHasCover) await deps.removeCover(post.id)
  } catch (e) {
    return failure(e, post)
  }
  const toast = saveToast(was, status)
  try {
    return { kind: 'saved', post, toast, posts: await deps.fetchPosts() }
  } catch (e) {
    return { kind: 'saved', post, toast, rereadError: e instanceof Error ? e.message : 'Could not reload the posts' }
  }
}

export type RemoveOutcome =
  | { kind: 'done'; toast: string; posts?: Post[]; rereadError?: string }
  | { kind: 'expired' }
  | { kind: 'forbidden' }
  | { kind: 'failed'; message: string }

export async function removePost(deps: { deletePost: (id: string) => Promise<void>; fetchPosts: () => Promise<Post[]> }, id: string): Promise<RemoveOutcome> {
  try {
    await deps.deletePost(id)
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return { kind: 'expired' }
    if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not delete the post' }
  }
  const toast = 'Post deleted'
  try {
    return { kind: 'done', toast, posts: await deps.fetchPosts() }
  } catch (e) {
    return { kind: 'done', toast, rereadError: e instanceof Error ? e.message : 'Could not reload the posts' }
  }
}

// The list after a delete: the re-read, else the old list without the deleted post (the delete succeeded).
export function postsAfterRemove(list: Post[], id: string, o: RemoveOutcome): Post[] {
  if (o.kind !== 'done') return list
  return o.posts ?? list.filter((p) => p.id !== id)
}

export type Applied = { editor: EditorState; toast: string | null; tone: ToastTone; posts?: Post[]; expire: boolean }

// `sent` is the state the save started from; edits made while it ran keep the editor dirty.
export function applySave(cur: EditorState, sent: EditorState, o: SaveOutcome): Applied {
  const red = (toast: string, editor = cur, expire = false): Applied => ({ editor, toast, tone: 'red', expire })
  const partial = (p?: Post) => (p ? partialEditor(cur, p) : cur)
  switch (o.kind) {
    case 'saved': {
      const editor = cur.draft === sent.draft ? savedEditor(cur, o.post) : partialEditor(cur, o.post, sent.draft.cover.kind !== 'none')
      if (o.rereadError !== undefined) return red(o.rereadError, editor)
      return { editor, toast: o.toast, tone: 'ok', posts: o.posts, expire: false }
    }
    case 'invalid':
      return red(o.toast, { ...cur, errors: o.errors, confirm: null })
    case 'expired':
      return { editor: partial(o.savedPost), toast: null, tone: 'red', expire: true }
    case 'forbidden':
      return red(STAFF_ONLY, partial(o.savedPost))
    case 'failed':
      return red(o.message, partial(o.savedPost))
  }
}

// The kept copy is written before the session ends, or the page leaves with the draft.
export function keepThenExpire(d: { write: (draft: Draft) => void; expire: () => void }, editor: EditorState | null): void {
  if (editor?.dirty) d.write(editor.draft)
  d.expire()
}

// The expiry for a callback that fires later: it keeps the editor as it is then.
export const keepOnExpire = (d: { write: (draft: Draft) => void; expire: () => void }, latest: () => EditorState | null) => (): void => keepThenExpire(d, latest())

export type AppliedRemove = { editor: EditorState | null; toast: string; tone: ToastTone; expire: boolean }

export function applyRemove(cur: EditorState, o: RemoveOutcome): AppliedRemove {
  switch (o.kind) {
    case 'done':
      return o.rereadError !== undefined ? { editor: null, toast: o.rereadError, tone: 'red', expire: false } : { editor: null, toast: o.toast, tone: 'ok', expire: false }
    case 'expired':
      return { editor: { ...cur, confirm: null }, toast: '', tone: 'red', expire: true }
    case 'forbidden':
      return { editor: { ...cur, confirm: null }, toast: STAFF_ONLY, tone: 'red', expire: false }
    case 'failed':
      return { editor: { ...cur, confirm: null }, toast: o.message, tone: 'red', expire: false }
  }
}

export type CatMoveOutcome =
  | { kind: 'moved'; cats: Category[] }
  | { kind: 'expired' }
  | { kind: 'forbidden' }
  | { kind: 'failed'; message: string; status?: number }

export async function moveCategoryFlow(deps: { moveCategory: (id: string, direction: 'up' | 'down') => Promise<MoveResult> }, id: string, direction: 'up' | 'down'): Promise<CatMoveOutcome> {
  try {
    return { kind: 'moved', cats: (await deps.moveCategory(id, direction)).categories }
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return { kind: 'expired' }
    if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not move the category', status: e instanceof ApiError ? (e.status ?? undefined) : undefined }
  }
}

export type CatRefreshOutcome = { kind: 'cats'; cats: Category[] } | Exclude<CatMoveOutcome, { kind: 'moved' }>

export async function refreshCategories(deps: { fetchCategories: () => Promise<Category[]> }): Promise<CatRefreshOutcome> {
  try {
    return { kind: 'cats', cats: await deps.fetchCategories() }
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return { kind: 'expired' }
    if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not load the categories' }
  }
}

export type AppliedCats = { cats?: Category[]; toast: string | null; tone: ToastTone; expire: boolean; reread: boolean }

export function applyCatMove(o: CatMoveOutcome | CatRefreshOutcome): AppliedCats {
  const base: AppliedCats = { toast: null, tone: 'red', expire: false, reread: false }
  switch (o.kind) {
    case 'moved':
    case 'cats':
      return { ...base, cats: o.cats }
    case 'expired':
      return { ...base, expire: true }
    case 'forbidden':
      return { ...base, toast: STAFF_ONLY }
    case 'failed':
      return { ...base, toast: o.message, reread: o.status === 404 }
  }
}

export const applyCatRefresh = applyCatMove

export type CatSaveOutcome =
  | { kind: 'saved'; toast: string; cats?: Category[]; rereadError?: string }
  | { kind: 'invalid'; error: string }
  | { kind: 'expired' }
  | { kind: 'forbidden' }
  | { kind: 'failed'; message: string; status?: number }

const statusOf = (e: unknown): number | undefined => (e instanceof ApiError ? (e.status ?? undefined) : undefined)

export async function saveCategory(
  deps: { addCategory: (i: CategoryInput) => Promise<Category>; updateCategory: (id: string, i: CategoryInput) => Promise<Category>; fetchCategories: () => Promise<Category[]> },
  form: CatForm,
): Promise<CatSaveOutcome> {
  const input = toCategoryInput(form)
  try {
    if (form.id === null) await deps.addCategory(input)
    else await deps.updateCategory(form.id, input)
  } catch (e) {
    const error = catFormError(e)
    if (error !== null) return { kind: 'invalid', error }
    if (statusOf(e) === 401) return { kind: 'expired' }
    if (statusOf(e) === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not save the category', status: statusOf(e) }
  }
  const toast = catSaveToast(form.id === null)
  try {
    return { kind: 'saved', toast, cats: await deps.fetchCategories() }
  } catch (e) {
    return { kind: 'saved', toast, rereadError: e instanceof Error ? e.message : 'Could not reload the categories' }
  }
}

export type CatRemoveOutcome =
  | { kind: 'done'; toast: string; move_to: string | null; cats?: Category[]; posts?: Post[]; rereadError?: string }
  | { kind: 'expired' }
  | { kind: 'forbidden' }
  | { kind: 'failed'; message: string; status?: number }

export async function removeCategory(
  deps: { deleteCategory: (id: string, moveTo: string | null) => Promise<DeleteResult>; fetchCategories: () => Promise<Category[]>; fetchPosts: () => Promise<Post[]> },
  cats: Category[],
  id: string,
  moveTo: string | null,
): Promise<CatRemoveOutcome> {
  const cat = cats.find((c) => c.id === id)
  let res: DeleteResult
  try {
    res = await deps.deleteCategory(id, cat && cat.post_count > 0 ? moveTo : null)
  } catch (e) {
    if (statusOf(e) === 401) return { kind: 'expired' }
    if (statusOf(e) === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not delete the category', status: statusOf(e) }
  }
  const target = cats.find((c) => c.id === res.move_to)?.name ?? null
  const done = { kind: 'done' as const, toast: catDeleteToast(cat?.name ?? 'Category', res.moved_posts, target), move_to: res.move_to }
  try {
    const [newCats, posts] = await Promise.all([deps.fetchCategories(), deps.fetchPosts()])
    return { ...done, cats: newCats, posts }
  } catch (e) {
    return { ...done, rereadError: e instanceof Error ? e.message : 'Could not reload the categories' }
  }
}

// The lists after a delete: the re-read, else the old lists with the category gone and its posts moved (the delete succeeded).
export function catsAfterRemove(cats: Category[], id: string, o: CatRemoveOutcome): Category[] {
  if (o.kind !== 'done') return cats
  return o.cats ?? cats.filter((c) => c.id !== id)
}

export function postsAfterCatRemove(posts: Post[], id: string, o: CatRemoveOutcome): Post[] {
  if (o.kind !== 'done') return posts
  return o.posts ?? posts.map((p) => (p.category_id === id ? { ...p, category_id: o.move_to } : p))
}

export function applyCatSave(ui: CatUi, o: CatSaveOutcome): AppliedCats & { ui: CatUi } {
  const base = { ui, toast: null, tone: 'red' as ToastTone, expire: false, reread: false }
  switch (o.kind) {
    case 'saved': {
      const closed = { ...ui, form: null }
      if (o.rereadError !== undefined) return { ...base, ui: closed, toast: o.rereadError }
      return { ...base, ui: closed, cats: o.cats, toast: o.toast, tone: 'ok' }
    }
    case 'invalid':
      return { ...base, ui: ui.form ? { ...ui, form: { ...ui.form, error: o.error } } : ui }
    case 'expired':
      return { ...base, expire: true }
    case 'forbidden':
      return { ...base, toast: STAFF_ONLY }
    case 'failed':
      return o.status === 404 ? { ...base, ui: { ...ui, form: null }, toast: o.message, reread: true } : { ...base, toast: o.message }
  }
}

export type CatLists = { cats: Category[]; posts: Post[] }

export function applyCatRemove(lists: CatLists, ui: CatUi, filters: BlogFilters, id: string, o: CatRemoveOutcome): AppliedCats & { ui: CatUi; cats: Category[]; posts: Post[]; filters: BlogFilters } {
  const base = { ...lists, ui: { ...ui, del: null }, filters, toast: null as string | null, tone: 'red' as ToastTone, expire: false, reread: false }
  switch (o.kind) {
    case 'done':
      return {
        ...base,
        cats: catsAfterRemove(lists.cats, id, o),
        posts: postsAfterCatRemove(lists.posts, id, o),
        filters: filters.cat === id ? { ...filters, cat: 'all' } : filters,
        toast: o.rereadError ?? o.toast,
        tone: o.rereadError !== undefined ? 'red' : 'ok',
      }
    case 'expired':
      return { ...base, ui, expire: true }
    case 'forbidden':
      return { ...base, toast: STAFF_ONLY }
    case 'failed':
      return { ...base, toast: o.message, reread: o.status === 404 || o.status === 422 }
  }
}
