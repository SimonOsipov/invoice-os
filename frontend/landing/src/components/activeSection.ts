// Pure active-section selector for the sticky top nav (LAND-01-02).
// Kept out of Nav.tsx so the scroll-spy decision can be unit-tested without a
// DOM: scroll math is a total function, whereas IntersectionObserver's answer
// depends on which entries happen to intersect and is undefined when none do.

// `sections` are viewport-relative tops in DOM order; the LAST one crossed wins.
// Returns null when nothing has crossed yet, or when the section that won has no
// nav link (the page has 8 section[id] but only 6 are nav targets). The
// membership test applies to the winner, not to the candidate set — filtering
// first would light a stale link while the visitor reads a non-nav section.
export function activeNavHref(
  sections: readonly { id: string; top: number }[],
  navHrefs: readonly string[],
  threshold: number,
): string | null {
  let winner: string | null = null
  for (const s of sections) {
    if (s.top <= threshold) winner = s.id
  }
  if (winner === null) return null
  const href = `#${winner}`
  return navHrefs.includes(href) ? href : null
}
