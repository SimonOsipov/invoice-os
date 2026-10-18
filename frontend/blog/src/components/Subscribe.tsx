import { useState, type CSSProperties } from 'react'
import type { Category } from '../data'
import { Icon } from '../icons'
import { subscribe, type Frequency } from '../subscribeApi'
import { Button } from './Button'
import { COLUMN, NewsletterShell, StatusTile } from './NewsletterShell'

const FREQS: { value: Frequency; label: string; note: string; phrase: string }[] = [
  { value: 'weekly', label: 'Weekly digest', note: "One email every Monday with the week's posts.", phrase: 'a weekly digest' },
  { value: 'every_post', label: 'Every post', note: 'An email each time a post is published in your topics.', phrase: 'an email for every new post' },
  { value: 'monthly', label: 'Monthly', note: 'One email on the first working day of each month.', phrase: 'a monthly email' },
]

const EMAIL_OK = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

const LABEL: CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  letterSpacing: '0.1em',
  textTransform: 'uppercase',
  color: 'var(--muted-foreground)',
}
const ERROR: CSSProperties = { fontSize: '12.5px', fontWeight: 600, color: 'var(--destructive)' }

export function Subscribe({ categories, onHome }: { categories: Category[]; onHome: () => void }) {
  const [email, setEmail] = useState('')
  const [topics, setTopics] = useState<string[] | null>(null)
  const [freq, setFreq] = useState<Frequency>('weekly')
  const [tried, setTried] = useState(false)
  const [sending, setSending] = useState(false)
  const [failed, setFailed] = useState(false)
  const [done, setDone] = useState(false)

  // Null means untouched: every category, however late the list arrives.
  const sel = topics ?? categories.map((c) => c.id)
  const chosen = categories.filter((c) => sel.includes(c.id)).map((c) => c.id)
  const emailOk = EMAIL_OK.test(email.trim())
  const emailErr = tried && !emailOk
  const topicErr = tried && chosen.length === 0
  const current = FREQS.find((f) => f.value === freq)!

  const submit = async () => {
    if (!emailOk || chosen.length === 0) {
      setTried(true)
      return
    }
    setTried(false)
    setFailed(false)
    setSending(true)
    try {
      await subscribe({ email: email.trim(), category_ids: chosen, frequency: freq })
      setDone(true)
    } catch {
      setFailed(true)
    } finally {
      setSending(false)
    }
  }

  return (
    <NewsletterShell>
      {done ? (
        <div style={{ ...COLUMN(16), animation: 'blogPop 320ms var(--ease-out)' }}>
          <StatusTile />
          <span style={{ fontSize: 24, fontWeight: 700, letterSpacing: '-0.03em' }}>Check your inbox.</span>
          <span className="t-body" style={{ color: 'var(--foreground)' }}>
            {`We sent a confirmation link to ${email.trim()}. Once confirmed, you will get ${current.phrase} across ${chosen.length} ${chosen.length === 1 ? 'topic' : 'topics'}.`}
          </span>
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginTop: 6 }}>
            <Button variant="primary" size="sm" onClick={onHome}>Back to the blog</Button>
            <Button variant="outline" size="sm" onClick={() => setDone(false)}>Change preferences</Button>
          </div>
        </div>
      ) : (
        <div style={COLUMN(22)}>
          <div style={COLUMN(6)}>
            <label htmlFor="blog-subscribe-email" style={LABEL}>Work email</label>
            <input
              id="blog-subscribe-email"
              className="blog-field"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="name@company.com"
              style={{
                height: 44,
                padding: '0 14px',
                borderRadius: 7,
                border: `1px solid ${emailErr ? 'var(--destructive)' : 'var(--input)'}`,
                background: '#fff',
                fontSize: '14.5px',
                color: 'var(--ink)',
                outline: 0,
              }}
            />
            {emailErr && (
              <span style={ERROR}>
                {email.trim() ? 'Enter a valid email address, such as name@company.com.' : 'Enter your work email.'}
              </span>
            )}
          </div>
          <div style={COLUMN(10)}>
            <span style={LABEL}>Topics</span>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              {categories.map((c) => {
                const on = sel.includes(c.id)
                return (
                  <button
                    key={c.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setTopics(on ? sel.filter((x) => x !== c.id) : [...sel, c.id])}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 7,
                      height: 34,
                      padding: '0 12px',
                      borderRadius: 7,
                      border: `1px solid ${on ? 'var(--primary)' : 'var(--input)'}`,
                      background: on ? 'var(--primary)' : 'var(--card)',
                      color: on ? '#fff' : 'var(--ink)',
                      fontSize: 13,
                      fontWeight: 600,
                      cursor: 'pointer',
                      transition: 'background 150ms, border-color 150ms',
                    }}
                  >
                    <Icon name={c.icon} size={14} />
                    <span>{c.name}</span>
                  </button>
                )
              })}
            </div>
            {topicErr && <span style={ERROR}>Choose at least one topic.</span>}
          </div>
          <div style={COLUMN(10)}>
            <span style={LABEL}>How often</span>
            <div
              style={{
                display: 'flex',
                padding: 3,
                gap: 2,
                background: 'var(--sage)',
                border: '1px solid var(--sage-border, #cedac7)',
                borderRadius: 6,
              }}
            >
              {FREQS.map((f) => {
                const on = f.value === freq
                return (
                  <button
                    key={f.value}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setFreq(f.value)}
                    style={{
                      flex: 1,
                      minWidth: 0,
                      height: 34,
                      border: 0,
                      borderRadius: 4,
                      cursor: 'pointer',
                      fontSize: 13,
                      fontWeight: 700,
                      background: on ? 'var(--primary)' : 'transparent',
                      color: on ? '#fff' : 'var(--tab-active-text)',
                      transition: 'background 150ms',
                    }}
                  >
                    {f.label}
                  </button>
                )
              })}
            </div>
            <span className="t-body-sm">{current.note}</span>
          </div>
          <div style={COLUMN(10)}>
            <Button variant="primary" arrow disabled={sending} onClick={submit} style={{ width: '100%' }}>Subscribe</Button>
            {failed && <span style={ERROR}>We could not send the confirmation email. Try again in a minute.</span>}
          </div>
        </div>
      )}
    </NewsletterShell>
  )
}
