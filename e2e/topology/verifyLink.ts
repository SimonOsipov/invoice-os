import { GATEWAY_URL } from './targets'

// internal/gateway/signin.go stateShape.
export const STATE_43 = /^[A-Za-z0-9_-]{43}$/
export const isConfirmPage = (u: URL, invite = false) =>
  u.origin === new URL(GATEWAY_URL).origin &&
  u.pathname === '/auth/verify' &&
  STATE_43.test(u.searchParams.get('state') ?? '') &&
  u.searchParams.has('invite') === invite
