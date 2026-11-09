import { SENT_NOTE, SENT_TEXT } from '../copy'

export function SentPanel({ email }: { email: string }) {
  return (
    <section className="check-card" role="status">
      <p>{SENT_TEXT(email)}</p>
      <p>{SENT_NOTE}</p>
    </section>
  )
}
