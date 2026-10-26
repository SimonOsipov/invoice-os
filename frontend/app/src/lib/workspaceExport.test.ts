// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'
import {
  EXPORT_COPY,
  mintExportCode,
  newExportState,
  startWorkspaceExport,
  submitExportForm,
} from './workspaceExport'

const base = 'https://gw.test'
const REFUSAL = 'an export of this workspace is already running'

interface Call {
  url: string
  opts?: { method?: string; body?: unknown }
}

function recorder(answer: (c: Call) => unknown): { f: AuthedFetch; calls: Call[] } {
  const calls: Call[] = []
  const f = (async (url: string, opts?: Call['opts']) => {
    const c = { url, opts }
    calls.push(c)
    return answer(c)
  }) as AuthedFetch
  return { f, calls }
}

afterEach(() => vi.restoreAllMocks())

describe('workspaceExport', () => {
  it('workspaceExport: the state is 43 base64url characters', () => {
    const a = newExportState()
    const b = newExportState()
    expect(a).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(b).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(a).not.toEqual(b)
  })

  it('workspaceExport: the state uses the url-safe alphabet for bytes that encode + and /', () => {
    vi.spyOn(crypto, 'getRandomValues').mockImplementation(((a: Uint8Array) => a.fill(0xfb)) as typeof crypto.getRandomValues)
    const s = newExportState()
    expect(s).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(s.startsWith('-_v7')).toBe(true)
  })

  it('workspaceExport: mintExportCode posts the state', async () => {
    const { f, calls } = recorder(() => ({ code: 'c' }))
    await expect(mintExportCode(f, base, 's')).resolves.toBe('c')
    expect(calls).toEqual([{ url: `${base}/auth/export-code`, opts: { method: 'POST', body: { state: 's' } } }])
  })

  it('workspaceExport: the form posts code and state to the redeem route', () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function (this: HTMLFormElement) {
      forms.push(this)
      seen.push(Object.fromEntries([...this.querySelectorAll('input')].map((i) => [i.name, i.value])))
      expect(document.body.contains(this)).toBe(true)
    })
    const forms: HTMLFormElement[] = []
    const seen: Record<string, string>[] = []
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const blobSpy = vi.spyOn(globalThis, 'Blob')

    submitExportForm(document, base, 'c', 's')

    expect(submit).toHaveBeenCalledTimes(1)
    const form = forms[0]
    expect(form.method).toBe('post')
    expect(form.getAttribute('action')).toBe(`${base}/auth/export-download`)
    expect(form.target).toBe('_self')
    expect(seen[0]).toEqual({ code: 'c', state: 's' })
    expect(document.body.contains(form)).toBe(false)
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(blobSpy).not.toHaveBeenCalled()
  })

  it('workspaceExport: the copy', () => {
    expect(EXPORT_COPY).toEqual({
      button: 'Download export',
      tick: 'I downloaded the export',
      started: "The download has started. Check your browser's downloads.",
    })
  })

  it('workspaceExport: the preflight runs first and a refusal stops the export', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    const ok = recorder((c) => (c.url.endsWith('/auth/export-code') ? { code: 'c' } : undefined))
    await startWorkspaceExport(ok.f, base, document)
    expect(ok.calls.map((c) => `${c.opts?.method ?? 'GET'} ${c.url}`)).toEqual([
      `GET ${base}/api/invoice/v1/workspace/export/preflight`,
      `POST ${base}/auth/export-code`,
    ])
    expect(submit).toHaveBeenCalledTimes(1)

    submit.mockClear()
    const refused = new ApiError('http', REFUSAL, 409)
    const no = recorder(() => {
      throw refused
    })
    await expect(startWorkspaceExport(no.f, base, document)).rejects.toBe(refused)
    expect(no.calls).toHaveLength(1)
    expect(submit).not.toHaveBeenCalled()
  })

  it('workspaceExport: the minted state is the state the form posts, and no URL carries the code or state', async () => {
    const forms: Record<string, string>[] = []
    const actions: string[] = []
    vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function (this: HTMLFormElement) {
      forms.push(Object.fromEntries([...this.querySelectorAll('input')].map((i) => [i.name, i.value])))
      actions.push(this.getAttribute('action') ?? '')
    })
    const { f, calls } = recorder((c) => (c.url.endsWith('/auth/export-code') ? { code: 'CODE' } : undefined))
    await startWorkspaceExport(f, base, document)
    const minted = (calls[1].opts?.body as { state: string }).state
    expect(minted).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(forms).toEqual([{ code: 'CODE', state: minted }])
    expect(actions).toEqual([`${base}/auth/export-download`])
    for (const c of calls) {
      expect(c.url).not.toContain('CODE')
      expect(c.url).not.toContain(minted)
    }
  })

  it('workspaceExport: a 403 preflight mints no code and a thrown submit still removes the form', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {
      throw new Error('boom')
    })
    const denied = new ApiError('http', 'only an admin can download the workspace export', 403)
    const no = recorder(() => {
      throw denied
    })
    await expect(startWorkspaceExport(no.f, base, document)).rejects.toBe(denied)
    expect(no.calls).toHaveLength(1)
    expect(submit).not.toHaveBeenCalled()

    const ok = recorder((c) => (c.url.endsWith('/auth/export-code') ? { code: 'c' } : undefined))
    await expect(startWorkspaceExport(ok.f, base, document)).rejects.toThrow('boom')
    expect(submit).toHaveBeenCalledTimes(1)
    expect(document.querySelectorAll('form')).toHaveLength(0)
  })
})
