// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { deletePost, fetchAuthors, fetchCategories, fetchCoverUrl, fetchPosts, type Post } from '../blogApi'
import { Blog } from './Blog'

vi.mock('../blogApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../blogApi')>()),
  fetchPosts: vi.fn(),
  fetchCategories: vi.fn(),
  fetchAuthors: vi.fn(),
  fetchCoverUrl: vi.fn(),
  deletePost: vi.fn(),
}))
vi.mock('../session', () => ({ withSession: <T,>(c: () => Promise<T>) => c(), expireOnce: vi.fn(), currentSub: () => 's1' }))

const POST: Post = {
  id: 'p1', category_id: null, publish_date: null, title: 'Cover race', summary: 's', author_id: null, author_name: null, status: 'draft', featured: false,
  body: [], cover: { content_type: 'image/jpeg', size_bytes: 1, updated_at: '2026-11-01T00:00:00Z' }, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
}
const FILTERS = { tab: 'posts', q: '', status: 'all', cat: 'all' } as const

const signals: AbortSignal[] = []
const mount = async () => {
  render(<Blog filters={FILTERS} onFilters={() => {}} onToast={() => {}} />)
  fireEvent.click(await screen.findByText('Cover race'))
  await waitFor(() => expect(signals.length).toBeGreaterThan(0))
}
const clickDelete = async () => {
  fireEvent.click(await screen.findByText('Delete'))
  fireEvent.click(await screen.findByText('Delete post'))
}

beforeEach(() => {
  signals.length = 0
  vi.mocked(fetchPosts).mockResolvedValue([POST])
  vi.mocked(fetchCategories).mockResolvedValue([])
  vi.mocked(fetchAuthors).mockResolvedValue([])
  vi.mocked(fetchCoverUrl).mockImplementation((_id, signal) => {
    signals.push(signal as AbortSignal)
    return new Promise(() => {})
  })
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('Blog cover read vs Delete', () => {
  it('Delete aborts the in-flight cover read before the DELETE is sent', async () => {
    let abortedAtDelete: boolean | null = null
    vi.mocked(deletePost).mockImplementation(async () => {
      abortedAtDelete = signals[0].aborted
    })
    await mount()
    await clickDelete()
    await waitFor(() => expect(deletePost).toHaveBeenCalledTimes(1))
    expect(abortedAtDelete).toBe(true)
  })

  it('a failed Delete leaves the editor open and starts a fresh, live cover read', async () => {
    vi.mocked(deletePost).mockRejectedValue(new Error('boom'))
    await mount()
    await clickDelete()
    await waitFor(() => expect(signals.length).toBe(2))
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
    expect(vi.mocked(fetchCoverUrl)).toHaveBeenLastCalledWith('p1', signals[1])
  })

  it('a successful Delete starts no second cover read', async () => {
    vi.mocked(deletePost).mockResolvedValue(undefined)
    await mount()
    await clickDelete()
    await waitFor(() => expect(screen.queryByText('Delete post')).toBeNull())
    expect(signals.length).toBe(1)
    expect(signals[0].aborted).toBe(true)
  })

  it('closing the editor aborts the cover read', async () => {
    await mount()
    expect(signals[0].aborted).toBe(false)
    fireEvent.click(screen.getAllByRole('button').find((b) => /back|posts/i.test(b.textContent ?? ''))!)
    await waitFor(() => expect(signals[0].aborted).toBe(true))
  })
})
