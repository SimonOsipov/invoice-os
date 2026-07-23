import { API_POINTS } from '../data'

export function Developers() {
  return (
    <section id="developers" style={{ background: 'var(--slate-900)', color: '#fff', borderBottom: '1px solid var(--slate-800)' }}>
      <div
        className="ios-grid ios-2"
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '88px 32px',
          display: 'grid',
          gridTemplateColumns: '0.95fr 1.05fr',
          gap: 56,
          alignItems: 'center',
        }}
      >
        <div>
          <div
            className="mono"
            style={{ fontSize: 11, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--teal-300)', marginBottom: 14, textTransform: 'uppercase' }}
          >
            / 05 — API &amp; INTEGRATIONS
          </div>
          <h2 style={{ fontSize: 38, lineHeight: 1.1, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 18px', color: '#fff' }}>
            Compliance as an API. Drop it into any ERP.
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.65, color: 'var(--slate-300)', margin: '0 0 28px' }}>
            REST endpoints, signed webhooks, OAuth2, and a sandbox MBS/FIRS adapter. Send invoice data in, get a
            validated, transmit-ready document back — with a full audit trail.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 30 }}>
            {API_POINTS.map((a, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 11 }}>
                <span style={{ color: 'var(--teal-300)' }}>{a.glyph}</span>
                <span style={{ fontSize: 14, color: 'var(--slate-200)' }}>{a.text}</span>
              </div>
            ))}
          </div>
          <a href="#demo" className="v2-btn" style={{ background: '#fff', color: 'var(--slate-900)', height: 44, padding: '0 20px' }}>
            Request API access
          </a>
        </div>

        {/* code block */}
        <div style={{ background: '#0c0e10', border: '1px solid var(--slate-800)', borderRadius: 'var(--radius-xl)', overflow: 'hidden' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 16px', borderBottom: '1px solid var(--slate-800)' }}>
            <span style={{ width: 10, height: 10, borderRadius: 99, background: '#3a3e40' }} />
            <span style={{ width: 10, height: 10, borderRadius: 99, background: '#3a3e40' }} />
            <span style={{ width: 10, height: 10, borderRadius: 99, background: '#3a3e40' }} />
            <span className="mono" style={{ fontSize: 11, color: 'var(--slate-400)', marginLeft: 8 }}>
              POST /v1/invoices/validate
            </span>
          </div>
          <pre className="mono" style={{ margin: 0, padding: 20, fontSize: 12.5, lineHeight: 1.75, color: 'var(--slate-200)', overflowX: 'auto' }}>
            <span style={{ color: 'var(--slate-500)' }}># Validate an invoice against Nigeria MBS rules</span>
            {'\ncurl https://api.ascomply.africa/v1/invoices/validate \\\n  -H '}
            <span style={{ color: 'var(--teal-300)' }}>"Authorization: Bearer sk_live_…"</span>
            {' \\\n  -d '}
            <span style={{ color: 'var(--teal-300)' }}>
              {'\'{ "buyer_tin": "12345678-0001",\n        "currency": "NGN",\n        "vat_rate": 7.5,\n        "lines": […] }\''}
            </span>
            {'\n\n'}
            <span style={{ color: 'var(--slate-500)' }}># ← 200 OK</span>
            {'\n{\n  '}
            <span style={{ color: '#8fd3bb' }}>"status"</span>
            {': '}
            <span style={{ color: '#e6b673' }}>"validated"</span>
            {',\n  '}
            <span style={{ color: '#8fd3bb' }}>"ready_to_transmit"</span>
            {': '}
            <span style={{ color: '#e6b673' }}>true</span>
            {',\n  '}
            <span style={{ color: '#8fd3bb' }}>"errors"</span>
            {': [],\n  '}
            <span style={{ color: '#8fd3bb' }}>"firs_reference"</span>
            {': '}
            <span style={{ color: '#e6b673' }}>"CSID-pending"</span>
            {'\n}'}
          </pre>
        </div>
      </div>
    </section>
  )
}
