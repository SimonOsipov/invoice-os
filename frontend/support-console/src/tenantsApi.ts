import { request } from './api'

const TENANTS_PATH = '/api/tenancy/v1/staff/tenants'

// Wire shapes: internal/tenancy/staff_tenants.go.
// StaffTenant
export interface StaffTenant {
  id: string
  name: string
  kind: string
  created_at: string
  demo: boolean
  active_members: number
  invoice_count: number
  last_activity_at: string | null
  deletion_due_at: string | null
  deletion_purge_started_at: string | null
}
// tenancy.Membership
export interface StaffMember {
  user_id: string
  role: string
  status: 'active' | 'invited' | 'suspended' | 'removed'
  display_name: string | null
  email: string | null
}
// StaffInvoice
export interface StaffInvoice {
  id: string
  invoice_number: string
  status: string
  created_at: string
}
// StaffTenantDetail
export interface StaffTenantDetail {
  tenant: StaffTenant
  members: StaffMember[]
  invoices: StaffInvoice[]
}

export const fetchStaffTenants = async (signal?: AbortSignal): Promise<StaffTenant[]> =>
  (await request<{ tenants: StaffTenant[] }>(TENANTS_PATH, { signal })).tenants

export const fetchStaffTenant = (id: string, signal?: AbortSignal): Promise<StaffTenantDetail> =>
  request<StaffTenantDetail>(`${TENANTS_PATH}/${encodeURIComponent(id)}`, { signal })

const tenantPath = (id: string): string => `${TENANTS_PATH}/${encodeURIComponent(id)}`
const memberDeletionPath = (id: string, userId: string): string => `${tenantPath(id)}/members/${encodeURIComponent(userId)}/deletion`
const post = <T>(path: string, body: unknown): Promise<T> => request<T>(path, { method: 'POST', body })

export const startTenantDeletion = (id: string, reason: string): Promise<{ deletion_due_at: string }> => post(`${tenantPath(id)}/deletion`, { reason })

export const cancelTenantDeletion = (id: string, reason: string): Promise<{ deletion_due_at: null }> => post(`${tenantPath(id)}/deletion/cancel`, { reason })

export const purgeTenant = (id: string, workspaceName: string, reason: string): Promise<{ deletion_purge_started_at: string }> =>
  post(`${tenantPath(id)}/deletion/purge`, { workspace_name: workspaceName, reason })

// tenancy.AccountDeletionPreview
export type AccountOutcome = 'leave' | 'hand_on' | 'delete_workspace'
export interface AccountPreview {
  outcome: AccountOutcome
  heir_name: string | null
  heir_user_id: string | null
}
export const previewMemberDeletion = (id: string, userId: string, signal?: AbortSignal): Promise<AccountPreview> =>
  request<AccountPreview>(memberDeletionPath(id, userId), { signal })

export const deleteMemberAccount = (id: string, userId: string, body: { outcome: 'leave' | 'hand_on'; heir_user_id?: string; reason: string }): Promise<{ outcome: AccountOutcome }> =>
  post(memberDeletionPath(id, userId), body)

export const filterTenants = (list: StaffTenant[], query: string): StaffTenant[] => {
  const q = query.trim().toLowerCase()
  return q === '' ? list : list.filter((t) => t.name.toLowerCase().includes(q))
}

// Mirrors the server's actor fallback (internal/actor/actor.go); '' falls through like null.
// A removed row never shows email or id: its name is display_name or "Deleted member".
export const memberName = (m: Pick<StaffMember, 'display_name' | 'email' | 'user_id' | 'status'>): string =>
  m.status === 'removed' ? m.display_name || 'Deleted member' : m.display_name || m.email || m.user_id

export const kindLabel = (kind: string): string => (kind === 'firm' ? 'FIRM' : kind === 'in_house' ? 'IN-HOUSE' : kind.toUpperCase())

// First letter of the first two words, upper-case; '?' for an empty name.
export const initials = (name: string): string =>
  name.trim().split(/\s+/).filter(Boolean).slice(0, 2).map((w) => Array.from(w)[0].toUpperCase()).join('') || '?'

export type DetailStatus = 'none' | 'loading' | 'ready' | 'not-found' | 'forbidden' | 'error'

// Ready and failed states count only for the tenant they belong to (failedId for errors).
export const detailStatus = (
  read: { status: string; data: StaffTenantDetail | null; error: { status: number | null } | null },
  tenantId: string | null,
  failedId: string | null,
): DetailStatus => {
  if (tenantId === null) return 'none'
  if (read.status === 'ready') return read.data?.tenant.id === tenantId ? 'ready' : 'loading'
  if (read.status !== 'error' || failedId !== tenantId) return 'loading'
  return read.error?.status === 404 ? 'not-found' : read.error?.status === 403 ? 'forbidden' : 'error'
}
