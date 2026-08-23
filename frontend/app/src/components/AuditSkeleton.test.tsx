// @vitest-environment jsdom
// AUDIT-06-08's RED specs.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { PlatformCtx } from '../types'

import { AUDIT_COLS, AUDIT_TABLE_MIN_WIDTH } from './AuditRow'
import { AuditSkeleton } from './AuditSkeleton'
import { AuditView } from './AuditView'

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('AuditSkeleton', () => {
  it('auditSkeleton_sharesTheTableGeometryConstant', () => {
    render(<AuditSkeleton />)
    const rows = screen.getAllByTestId('audit-skeleton-row')
    expect(rows.length).toBeGreaterThan(0)
    // Resolved style, not the presence of a constant: a skeleton that restated the
    // template would still read as "shares it" to a source scan, then jump when data
    // landed.
    for (const r of rows) {
      expect(r.style.gridTemplateColumns).toBe(AUDIT_COLS)
      expect(r.style.minWidth).toBe(`${AUDIT_TABLE_MIN_WIDTH}px`)
    }
    // The second half: a byte-identical copy of the template passes the check above and
    // then drifts the first time either file is edited. Only one of them may own it.
    const src = readFileSync(join(__dirname, 'AuditSkeleton.tsx'), 'utf8')
    expect(src, 'the source scan read nothing').toContain('AuditSkeleton')
    expect(src).toContain('AUDIT_COLS')
    expect(src).not.toContain('minmax(190px')
  })

  it('auditSkeleton_isNotASpinner', async () => {
    // A fetch that never settles holds the screen in its loading rung.
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const ctx = {
      mode: 'firm',
      active: { entityId: 'ent-1' },
      user: { tenantName: 'Acme Co' },
      authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    } as unknown as PlatformCtx
    render(<AuditView ctx={ctx} />)

    // The real table chrome, so nothing moves when the rows arrive.
    await waitFor(() => expect(screen.getByTestId('audit-table-head')).toBeTruthy())
    expect(screen.getAllByTestId('audit-skeleton-row').length).toBeGreaterThan(0)
    expect(screen.queryByText(/loading/i)).toBeNull()
  })
})
