const TOOLS = ['SAP', 'Oracle NetSuite', 'Sage', 'QuickBooks', 'Zoho Books', 'CSV / XLSX']

export function TrustStrip() {
  return (
    <section style={{ borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
      <div
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '22px 32px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 32,
          flexWrap: 'wrap',
        }}
      >
        <span className="label">Built for the way Nigerian finance already works</span>
        <div style={{ display: 'flex', alignItems: 'center', gap: 36, flexWrap: 'wrap' }}>
          {TOOLS.map((t) => (
            <span key={t} className="mono" style={{ fontSize: 13, color: 'var(--fg-2)', fontWeight: 500 }}>
              {t}
            </span>
          ))}
        </div>
      </div>
    </section>
  )
}
