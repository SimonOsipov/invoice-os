// The demo module's own icons: the shared glyphs carry these same paths at production sizes,
// and re-exporting demo-only sizes from glyphs.tsx would keep them in a flag-off bundle.

import { Icon } from '../icons'

export const demoFlaskGlyph = <Icon paths={['M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3', 'M7.5 14h9']} size={12} strokeWidth={1.8} />
export const demoLockGlyph = <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={13} strokeWidth={1.7} />
export const demoTickGlyph = <Icon paths={['M20 6 9 17l-5-5']} size={14} strokeWidth={2.2} />
export const demoChevronUpGlyph = <Icon paths={['m18 15-6-6-6 6']} size={15} strokeWidth={2} />
export const demoCloseGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={12} strokeWidth={2.2} />
