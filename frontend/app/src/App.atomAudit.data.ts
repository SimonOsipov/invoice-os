// Row shape for the Workspace atom-drift guard (App.atomAudit.test.tsx). AUDITED_ATOMS
// itself -- the 38-row audit table -- is authored next; this file only fixes its shape.

export type Verdict = 'stale-and-reachable' | 'stale-but-unreachable' | 'correctly-reset' | 'deliberate'

export type AuditedAtom = {
  // Verbatim text extracted by the two regexes in App.atomAudit.test.tsx: an
  // `[x, setX]` pair for a useState atom, or the bare name for reqInFlight (useRef).
  binding: string
  name: string
  line: number
  kind: 'useState' | 'useRef'
  resetBySwitchClient: boolean
  routes: string[]
  verdict: Verdict
  // Line the PR cites as evidence for this row's verdict, e.g. filing's comment inside
  // switchClient (App.tsx:662-665).
  citationLine?: number
  note: string
}
