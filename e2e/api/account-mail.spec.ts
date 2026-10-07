// Account-mail template and logo, served by the deployed gateway.
// Plain fetch, not rawFetch: rawFetch exposes no content type and no bytes.
import { test, expect } from '@playwright/test'
import { apiBase } from './client'

// internal/gateway/emails.go MailTemplate: the template's content type.
const HTML_CONTENT_TYPE = 'text/html; charset=utf-8'
// internal/gateway/emails.go MailLogo: the logo's content type.
const PNG_CONTENT_TYPE = 'image/png'
const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]

test.describe('account mail (API E2E, over the deployed gateway)', () => {
  test('account mail: the gateway serves the confirmation template', async () => {
    const res = await fetch(`${apiBase()}/emails/confirmation.html`)
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe(HTML_CONTENT_TYPE)
    const body = await res.text()
    expect(body.length).toBeGreaterThan(0)
    // The raw template, not a rendered mail: GoTrue renders it per user.
    expect(body).toContain('define "layout"')
    expect(body).toContain('.ConfirmationURL')
  })

  test('account mail: the gateway serves the recovery template', async () => {
    const res = await fetch(`${apiBase()}/emails/recovery.html`)
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe(HTML_CONTENT_TYPE)
    expect(await res.text()).toContain('{{ .ConfirmationURL }}')
  })

  test('account mail: the gateway serves the logo', async () => {
    const res = await fetch(`${apiBase()}/emails/mark.png`)
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe(PNG_CONTENT_TYPE)
    const bytes = new Uint8Array(await res.arrayBuffer())
    expect(bytes.length).toBeGreaterThan(PNG_SIGNATURE.length)
    expect(Array.from(bytes.slice(0, PNG_SIGNATURE.length))).toEqual(PNG_SIGNATURE)
  })

  test('account mail: an unknown template path is a 404, not a page', async () => {
    const res = await fetch(`${apiBase()}/emails/nonexistent.html`)
    expect(res.status).toBe(404)
    expect(res.headers.get('content-type') ?? '').not.toContain('text/html')
  })
})
