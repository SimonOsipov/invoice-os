const AUDIENCES = [
  'Medium taxpayers',
  'Accounting firms',
  'ERP consultants',
  'Distributors',
  'Manufacturers',
  'Formal SMEs',
  'Fintech',
  'CRMs',
]

export function TrustStrip() {
  // data-strip: stable selector for the audience-strip test oracle
  return (
    <section data-strip="audience" style={{ borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
      <div
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '22px 32px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          columnGap: 36,
          rowGap: 12,
          flexWrap: 'wrap',
        }}
      >
        {AUDIENCES.map((a) => (
          <span key={a} className="label" style={{ fontSize: 12 }}>
            {a}
          </span>
        ))}
      </div>
    </section>
  )
}
