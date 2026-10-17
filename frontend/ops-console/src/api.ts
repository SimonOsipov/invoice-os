import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client'
import { loadConsoleSession } from '@invoice-os/console-session'
import { SESSION_KEY } from './auth'

export const request = <T>(path: string, opts: { method?: string; body?: unknown; signal?: AbortSignal } = {}): Promise<T> => {
  const base = gatewayBase()
  if (!base) return Promise.reject(new ApiError('network', 'Gateway URL is not configured'))
  const token = loadConsoleSession(SESSION_KEY)?.token
  if (!token) return Promise.reject(new ApiError('http', 'Not signed in', 401))
  return apiFetch<T>(base + path, { ...opts, token })
}
