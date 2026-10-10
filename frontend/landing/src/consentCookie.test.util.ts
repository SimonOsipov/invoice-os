// Vitest setup: the consent cookie outlives a test inside a jsdom file, so expire it after each.
import { afterEach } from 'vitest'

afterEach(() => {
  if (!globalThis.document) return
  document.cookie = 'asc_consent=; Max-Age=0; Path=/; Domain=ascomply.com'
  document.cookie = 'asc_consent=; Max-Age=0; Path=/'
})
