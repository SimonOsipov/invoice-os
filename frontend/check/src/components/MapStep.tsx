import { CHECK_FIELD_KEYS, canSubmitMapping, fieldLabel, previewColumns, type Mapping, type Suggestion } from '../fields'

interface Props {
  fileName: string
  suggestion: Suggestion
  fields: Mapping
  onField: (key: string, header: string | null) => void
  onBack: () => void
  onContinue: () => void
}

export function MapStep({ fileName, suggestion, fields, onField, onBack, onContinue }: Props) {
  const columns = previewColumns(suggestion, 3)
  return (
    <section className="check-card">
      <h2>Match your columns</h2>
      <p className="check-file">{fileName}</p>
      <div className="check-scroll">
        <table className="check-columns">
          <thead>
            <tr>
              <th scope="col">Column</th>
              <th scope="col">Header</th>
              <th scope="col">Examples</th>
            </tr>
          </thead>
          <tbody>
            {columns.map((c, i) => (
              <tr key={i}>
                <td>{c.letter}</td>
                <td>{c.header}</td>
                <td>
                  {c.samples.map((s, j) => (
                    <div key={j}>{s}</div>
                  ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="check-fields">
        {CHECK_FIELD_KEYS.map((key) => (
          <div key={key} className="check-field">
            <label htmlFor={`check-field-${key}`} className={key === 'invoice_number' ? 'check-label check-required' : 'check-label'}>
              {fieldLabel(key)}
            </label>
            <select id={`check-field-${key}`} value={fields[key] ?? ''} onChange={(e) => onField(key, e.target.value === '' ? null : e.target.value)}>
              <option value="">Not in this file</option>
              {columns
                .filter((c) => c.mappable)
                .map((c, i) => (
                  <option key={i} value={c.header}>
                    {c.header}
                  </option>
                ))}
            </select>
          </div>
        ))}
      </div>
      <p className="check-hint">A supplier name or TIN that is not in this file fails its rule in your results.</p>
      <div className="check-actions">
        <button type="button" className="check-btn check-btn--outline" onClick={onBack}>
          Choose another file
        </button>
        <button type="button" className="check-btn" disabled={!canSubmitMapping(fields)} onClick={onContinue}>
          Continue
        </button>
      </div>
    </section>
  )
}
