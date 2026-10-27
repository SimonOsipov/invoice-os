import type { AuthedFetch } from './portfolio'
import { mintState } from './signInState'

// Copy matches the delete-dialog mock.
export const EXPORT_COPY = {
  button: 'Download export',
  tick: 'I downloaded the export',
  started: "The download has started. Check your browser's downloads.",
} as const

// The sign-in state generator: 43 base64url characters, the gateway's `stateShape`.
export const newExportState = mintState

// GET .../workspace/export/preflight (WorkspaceExportPreflightHandler): 204 resolves undefined; 403/409 reject.
export function preflightExport(f: AuthedFetch, base: string): Promise<void> {
  return f<void>(`${base}/api/invoice/v1/workspace/export/preflight`)
}

// POST /auth/export-code (ExportCodeHandler): `{state}` in, `{code}` out.
export async function mintExportCode(f: AuthedFetch, base: string, state: string): Promise<string> {
  const res = await f<{ code: string }>(`${base}/auth/export-code`, { method: 'POST', body: { state } })
  return res.code
}

// A form navigation, so the browser streams the ZIP to disk and the page never holds it.
// POST /auth/export-download (ExportDownloadHandler) reads the form fields `code` and `state`.
export function submitExportForm(doc: Document, base: string, code: string, state: string): void {
  const form = doc.createElement('form')
  form.method = 'POST'
  form.action = `${base}/auth/export-download`
  form.target = '_self'
  form.hidden = true
  for (const [name, value] of [
    ['code', code],
    ['state', state],
  ]) {
    const input = doc.createElement('input')
    input.type = 'hidden'
    input.name = name
    input.value = value
    form.appendChild(input)
  }
  doc.body.appendChild(form)
  try {
    form.submit()
  } finally {
    form.remove()
  }
}

export async function startWorkspaceExport(f: AuthedFetch, base: string, doc: Document = document): Promise<void> {
  await preflightExport(f, base)
  const state = newExportState()
  const code = await mintExportCode(f, base, state)
  submitExportForm(doc, base, code, state)
}
