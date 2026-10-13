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
}
// tenancy.Membership
export interface StaffMember {
  user_id: string
  role: string
  status: 'active' | 'invited' | 'suspended'
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

export const filterTenants = (list: StaffTenant[], query: string): StaffTenant[] => {
  const q = query.trim().toLowerCase()
  return q === '' ? list : list.filter((t) => t.name.toLowerCase().includes(q))
}

// Mirrors the server's actor fallback (internal/actor/actor.go); '' falls through like null.
export const memberName = (m: Pick<StaffMember, 'display_name' | 'email' | 'user_id'>): string => m.display_name || m.email || m.user_id

export const kindLabel = (kind: string): string => (kind === 'firm' ? 'FIRM' : kind === 'in_house' ? 'IN-HOUSE' : kind.toUpperCase())
