import { useEffect, useRef, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading } from '@invoice-os/api-client'

import { plusGlyph } from '../glyphs'
import { firstRunSurface } from '../lib/clients'
import { companySetupAccess } from '../lib/members'
import { listEntities } from '../lib/portfolio'
import type { Mode, PlatformCtx } from '../types'
import { EntityFormModal } from './EntityFormModal'

export const ADD_COMPANY_COPY: Record<Mode, { h1: string; emptyTitle: string; emptyMessage: string; button: string }> = {
  inhouse: {
    h1: 'Add your company',
    emptyTitle: 'No company yet',
    emptyMessage: "Invoices are filed for a registered company. Add yours — you'll need its name and its TIN.",
    button: 'Add company',
  },
  firm: {
    h1: 'Add your first client',
    emptyTitle: 'No clients yet',
    emptyMessage: "Invoices are filed for a registered company. Add the first client you file for — you'll need its name and its TIN.",
    button: 'Add client',
  },
}

export const NO_COMPANY_COPY = {
  title: 'No company created',
  message: 'Your workspace admin adds the company. You can start when it exists.',
}

function CompanySetupWaiting({ ctx, base, onCompanyFound }: { ctx: PlatformCtx; base: string | null; onCompanyFound: () => void }) {
  const latest = useRef({ authedFetch: ctx.authedFetch, onCompanyFound })
  latest.current = { authedFetch: ctx.authedFetch, onCompanyFound }

  // The admin may add the company while this tab is in the background.
  useEffect(() => {
    let live = true
    const onVisible = () => {
      if (document.visibilityState !== 'visible' || base == null) return
      listEntities(latest.current.authedFetch, base)
        .then((r) => {
          if (live && r.entities.length > 0) latest.current.onCompanyFound()
        })
        .catch(() => {})
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      live = false
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [])

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 26 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          OVERVIEW
        </div>
        <h1 style={{ fontSize: 28, letterSpacing: '-0.03em', margin: '0 0 5px' }}>{NO_COMPANY_COPY.title}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0, overflowWrap: 'anywhere' }}>
          {ctx.user.tenantName ?? 'Your workspace'} · invoices are filed for a registered company.
        </p>
      </div>
      <div data-testid="company-setup-waiting">
        <EmptyState dense messageMaxWidth={460} message={NO_COMPANY_COPY.message} />
      </div>
    </div>
  )
}

export function AddCompanyTask({ ctx }: { ctx: PlatformCtx }) {
  const { mode, activeEntity, entitiesState, entities, clients, refetchEntities } = ctx
  const [open, setOpen] = useState(false)
  const [companyFound, setCompanyFound] = useState(false)
  const base = gatewayBase()
  const surface = firstRunSurface(activeEntity, entitiesState, entities.length, clients.length)

  if (surface === 'error' && ctx.entitiesError) {
    return (
      <div style={{ padding: '30px 36px 56px' }}>
        <ErrorState error={ctx.entitiesError} onRetry={refetchEntities} />
      </div>
    )
  }
  const access = companySetupAccess(ctx.membersState, ctx.members)
  const waiting = (
    <CompanySetupWaiting
      ctx={ctx}
      base={base}
      onCompanyFound={() => {
        setCompanyFound(true)
        refetchEntities()
      }}
    />
  )
  // a found company keeps the waiting view until the dashboard replaces it
  if (surface === 'loading' && companyFound) return waiting
  if (surface !== 'task') {
    return (
      <div style={{ padding: '30px 36px 56px' }}>
        <Loading label="Loading your workspace…" />
      </div>
    )
  }

  if (access === 'error' && ctx.membersError) {
    return (
      <div style={{ padding: '30px 36px 56px' }}>
        <ErrorState error={ctx.membersError} onRetry={ctx.refetchMembers} />
      </div>
    )
  }
  if (access === 'error' || access === 'loading') {
    return (
      <div style={{ padding: '30px 36px 56px' }}>
        <Loading label="Loading your workspace…" />
      </div>
    )
  }
  if (access === 'wait') return waiting

  const copy = ADD_COMPANY_COPY[mode]
  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 26 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          OVERVIEW
        </div>
        <h1 style={{ fontSize: 28, letterSpacing: '-0.03em', margin: '0 0 5px' }}>{copy.h1}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0, overflowWrap: 'anywhere' }}>
          {ctx.user.tenantName ?? 'Your workspace'} · invoices are filed for a registered company.
        </p>
      </div>
      <div data-testid="add-company-task">
        <EmptyState
          dense
          messageMaxWidth={460}
          title={copy.emptyTitle}
          message={copy.emptyMessage}
          action={
            <button onClick={() => setOpen(true)} disabled={base == null} className="v2-btn v2-btn-primary pf-btn">
              <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> {copy.button}
            </button>
          }
        />
      </div>
      {open && base != null && (
        <EntityFormModal
          mode="create"
          ctx={ctx}
          base={base}
          onClose={() => setOpen(false)}
          onSuccess={() => {
            refetchEntities()
            setOpen(false)
          }}
        />
      )}
    </div>
  )
}
