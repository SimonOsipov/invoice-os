import { READING_TEXT } from '../copy'
import { ACCEPT } from '../fields'
import type { Refusal as RefusalValue } from '../messages'
import { Refusal } from './Refusal'

interface Props {
  fileName: string | null
  reading: boolean
  verdict: string
  refusal: RefusalValue | null
  onPick: (file: File) => void
}

export function UploadStep({ fileName, reading, verdict, refusal, onPick }: Props) {
  return (
    <section className="check-card">
      <label htmlFor="check-file" className="check-label">
        Invoice file
      </label>
      <input
        id="check-file"
        type="file"
        accept={ACCEPT}
        disabled={reading}
        onChange={(e) => {
          const file = e.target.files?.[0]
          e.target.value = ''
          if (file) onPick(file)
        }}
      />
      {fileName && <p className="check-file">{fileName}</p>}
      {reading && <p role="status">{READING_TEXT}</p>}
      {verdict && (
        <p role="alert" className="check-refusal">
          {verdict}
        </p>
      )}
      {refusal && <Refusal refusal={refusal} />}
    </section>
  )
}
