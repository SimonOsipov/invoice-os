import { useEffect, useRef, useState } from 'react'
import { DROP_BUTTON, DROP_HELP, DROP_OR, DROP_TITLE, DROP_TITLE_OVER, FILE_LABEL, FILE_PRIVACY, FILE_TYPE_NAMES, READING_TEXT, UPLOAD_STEP_NO, UPLOAD_TITLE } from '../copy'
import { GLYPHS, Icon, IconTile } from '../ds'
import { ACCEPT, formatBytes } from '../fields'
import { CHECK_GLYPHS } from '../glyphs'
import type { Refusal } from '../messages'

interface Props {
  file: { name: string; size: number } | null
  reading: boolean
  verdict: string
  refusal: Refusal | null
  onPick: (file: File) => void
}

// A dragged text carries no 'Files' type and must not count.
const hasFiles = (e: { dataTransfer: DataTransfer | null }) => e.dataTransfer?.types?.includes('Files') ?? false

export function UploadStep({ file, reading, verdict, refusal, onPick }: Props) {
  const [over, setOver] = useState(false)
  // Depth, not a flag: dragleave also fires when the pointer crosses a child.
  const depth = useRef(0)
  const inputRef = useRef<HTMLInputElement>(null)
  // The input unmounts while reading; a read started from it hands focus back when the zone returns.
  const refocus = useRef(false)
  const error = verdict || refusal?.text || ''

  // A file dropped beside the zone must not open in the tab (it would drop the read in flight).
  useEffect(() => {
    const stop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      // Off the zone the cursor must say no; the zone keeps the browser's copy effect.
      if (e.dataTransfer && !(e.target as Element | null)?.closest?.('.check-drop')) e.dataTransfer.dropEffect = 'none'
    }
    window.addEventListener('dragover', stop)
    window.addEventListener('drop', stop)
    return () => {
      window.removeEventListener('dragover', stop)
      window.removeEventListener('drop', stop)
    }
  }, [])

  useEffect(() => {
    if (!reading && refocus.current) {
      refocus.current = false
      inputRef.current?.focus()
    }
  }, [reading])

  return (
    <section className="check-card check-upload">
      <div className="check-step-head">
        <span className="check-mono check-step-no">{UPLOAD_STEP_NO}</span>
        <h2 className="check-step-title">{UPLOAD_TITLE}</h2>
      </div>
      <div className={reading ? 'check-upload-read' : 'check-upload-field'}>
        {!reading && (
          <>
            <label htmlFor="check-file" className="check-label">
              {FILE_LABEL}
            </label>
            <label
              className={`check-drop${over ? ' is-drag' : error ? ' is-err' : ''}`}
              onDragEnter={(e) => {
                if (!hasFiles(e)) return
                e.preventDefault()
                depth.current++
                setOver(true)
              }}
              onDragOver={(e) => {
                if (hasFiles(e)) e.preventDefault()
              }}
              onDragLeave={(e) => {
                if (!hasFiles(e)) return
                if (--depth.current <= 0) {
                  depth.current = 0
                  setOver(false)
                }
              }}
              onDrop={(e) => {
                if (hasFiles(e)) e.preventDefault()
                refocus.current = false
                depth.current = 0
                setOver(false)
                const dropped = e.dataTransfer?.files?.[0]
                if (dropped) onPick(dropped)
              }}
            >
              <input
                ref={inputRef}
                id="check-file"
                type="file"
                className="check-sr"
                accept={ACCEPT}
                aria-describedby="check-file-help check-file-err"
                onChange={(e) => {
                  const picked = e.target.files?.[0]
                  e.target.value = ''
                  if (picked) {
                    refocus.current = true
                    onPick(picked)
                  }
                }}
              />
              <span className="check-drop-icon" aria-hidden="true">
                <span className="check-drop-arrow">
                  <Icon paths={CHECK_GLYPHS['arrow-right']} size={24} strokeWidth={2} />
                </span>
              </span>
              <span className="check-drop-text">
                <span className="check-drop-title">{over ? DROP_TITLE_OVER : DROP_TITLE}</span>
                <span className="check-drop-or">{DROP_OR}</span>
              </span>
              <span className="check-fake-btn" aria-hidden="true">
                {DROP_BUTTON}
              </span>
              <span id="check-file-help" className="check-drop-help">
                <span className="check-chips">
                  {FILE_TYPE_NAMES.map((n) => (
                    <span key={n} className="check-chip">
                      {n}
                    </span>
                  ))}
                </span>
                <span className="check-drop-note">{DROP_HELP}</span>
              </span>
            </label>
            {error && (
              <p id="check-file-err" role="alert" className="check-err">
                <Icon paths={GLYPHS['triangle-alert']} size={16} strokeWidth={2} />
                <span>{error}</span>
              </p>
            )}
          </>
        )}
        {reading && (
          <>
            <span className="check-label">{FILE_LABEL}</span>
            <div className="check-upload-file">
              <IconTile name="file-text" size={40} />
              <div className="check-upload-meta">
                <span className="check-upload-name">{file?.name}</span>
                <span className="check-upload-size">{file ? formatBytes(file.size) : ''}</span>
              </div>
              <span className="check-spinner" aria-hidden="true" />
            </div>
            <div className="check-bar" aria-hidden="true">
              <i />
            </div>
          </>
        )}
        <p role="status" className={reading ? 'check-upload-reading' : 'check-sr'}>
          {reading ? READING_TEXT : ''}
        </p>
      </div>
      <p className="check-upload-note">
        <span className="check-upload-shield">
          <Icon paths={GLYPHS['shield-check']} size={16} strokeWidth={2} />
        </span>
        <span>{FILE_PRIVACY}</span>
      </p>
    </section>
  )
}
