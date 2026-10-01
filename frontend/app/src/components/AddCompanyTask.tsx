// Stub (AUTH-10-03 Mode A): the executor replaces the body.
import type { Mode, PlatformCtx } from '../types'

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

export function AddCompanyTask(_props: { ctx: PlatformCtx }) {
  return null
}
