// Landing → app hand-off code redemption (AUTH-05 D8, D9, D25). D17 stub: signatures only.
import { APP_PERSONAS, type Me, type Persona, type Session } from '../auth'

export const HANDOFF_PARAM = 'handoff'

export function readHandoffCode(_search: string): string | null {
  return null
}

export function handoffPersona(_me: Me): Persona {
  return APP_PERSONAS.firm
}

export function redeemHandoff(_base: string, _code: string, _state: string): Promise<Session> {
  return Promise.reject(new Error('not implemented'))
}

export function isLiveHandoffSession(_session: Session | null, _now: number = Date.now()): boolean {
  return false
}
