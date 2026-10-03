import { API_BULLETS } from '../data'
import { GLYPHS, Icon } from '../icons'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'

const KEY = { color: 'var(--eyebrow-on-dark)' }
const NOTE = { color: 'var(--surface-body)' }
const VAL = { color: 'var(--accent)' }

export function Api({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <Section tone="dark" id="api">
      <div
        className="split"
        style={{ gridTemplateColumns: 'minmax(0, 0.95fr) minmax(0, 1.05fr)', gap: 'clamp(32px, 5vw, 64px)', alignItems: 'center' }}
      >
        <div style={{ display: 'grid', gap: 22, justifyItems: 'start' }}>
          <Eyebrow tone="dark">API &amp; INTEGRATIONS</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0, color: 'var(--surface-foreground)' }}>
            Compliance as an API.
            <br /> <span className="t-hl-dark2">Drop it into any ERP.</span>
          </h2>
          <p className="t-body" style={{ margin: 0, maxWidth: 480, color: 'var(--surface-body)' }}>
            REST endpoints, signed webhooks, OAuth2 and a sandbox MBS/NRS adapter. Send invoice data in, get a validated, submission-ready document
            back, with a full audit trail.
          </p>
          <div style={{ display: 'grid', gap: 12 }}>
            {API_BULLETS.map((b) => (
              <div key={b.icon} style={{ display: 'flex', alignItems: 'center', gap: 12, color: 'var(--accent)' }}>
                <Icon paths={GLYPHS[b.icon]} size={18} />
                <span style={{ fontSize: 14, lineHeight: 1.5, color: 'var(--surface-foreground)' }}>{b.text}</span>
              </div>
            ))}
          </div>
          <Button variant="accent" onClick={onBookDemo}>
            Request API access
          </Button>
        </div>
        <div
          data-api-panel
          style={{
            background: 'var(--surface-panel)',
            border: '1px solid var(--surface-panel-border)',
            borderRadius: 'var(--radius-md)',
            overflow: 'hidden',
            minWidth: 0,
          }}
        >
          <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--surface-panel-border)' }}>
            <span className="t-meta" style={{ color: 'var(--eyebrow-on-dark)' }}>
              POST /v1/invoices/validate
            </span>
          </div>
          <pre
            className="api-sample"
            tabIndex={0}
            role="region"
            aria-label="Sample API request and response"
            style={{
              margin: 0,
              padding: '22px 20px',
              fontFamily: 'var(--font-mono)',
              fontSize: 12.5,
              lineHeight: 1.75,
              color: 'var(--surface-foreground)',
              overflowX: 'auto',
            }}
          >
            <span style={NOTE}># Validate an invoice against Nigeria MBS rules</span>
            {'\ncurl https://api.ascomply.africa/v1/invoices/validate \\\n  -H '}
            <span style={KEY}>"Authorization: Bearer sk_live_..."</span>
            {' \\\n  -d '}
            <span style={KEY}>{`'{ "buyer_tin": "12345678-0001",\n        "currency": "NGN",\n        "vat_rate": 7.5,\n        "lines": [...] }'`}</span>
            {'\n\n'}
            <span style={NOTE}># 200 OK</span>
            {'\n{\n  '}
            <span style={KEY}>"status"</span>
            {': '}
            <span style={VAL}>"validated"</span>
            {',\n  '}
            <span style={KEY}>"ready_to_submit"</span>
            {': '}
            <span style={VAL}>true</span>
            {',\n  '}
            <span style={KEY}>"errors"</span>
            {': [],\n  '}
            <span style={KEY}>"nrs_reference"</span>
            {': '}
            <span style={VAL}>"pending"</span>
            {'\n}'}
          </pre>
        </div>
      </div>
    </Section>
  )
}
