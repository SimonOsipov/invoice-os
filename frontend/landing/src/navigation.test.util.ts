// Reads delegate to the real location so the boot strip still works; only `href` writes are captured.
export function captureNavigation(): { assigned: string[]; restore: () => void } {
  const assigned: string[] = []
  const original = Object.getOwnPropertyDescriptor(window, 'location')
  const real = window.location
  const stub = {
    get href() {
      return real.href
    },
    set href(v: string) {
      assigned.push(v)
    },
    get search() {
      return real.search
    },
    get pathname() {
      return real.pathname
    },
    get hash() {
      return real.hash
    },
    get origin() {
      return real.origin
    },
  }
  Object.defineProperty(window, 'location', { value: stub, writable: true, configurable: true })
  return {
    assigned,
    restore: () => {
      if (original) Object.defineProperty(window, 'location', original)
    },
  }
}
