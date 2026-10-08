import { useSyncExternalStore } from 'react'

// Must equal the one @media query in library.css and the token flip in spacing.css (PH-01, PH-02).
export const PHONE_MAX_WIDTH = 767

const QUERY = `(max-width: ${PHONE_MAX_WIDTH}px)`
const mql = () => (typeof window.matchMedia === 'function' ? window.matchMedia(QUERY) : null)

const subscribe = (cb: () => void) => {
  const m = mql()
  m?.addEventListener('change', cb)
  return () => m?.removeEventListener('change', cb)
}

export const usePhone = (): boolean => useSyncExternalStore(subscribe, () => mql()?.matches ?? false)
