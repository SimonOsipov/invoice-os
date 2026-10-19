import { useRef, useState } from 'react'
import { ApiError } from '@invoice-os/api-client/client'
import { clearConfirmToken } from '../confirmLink'
import { confirmSubscription } from '../subscribeApi'
import { Button } from './Button'
import { COLUMN, NewsletterShell, StatusTile } from './NewsletterShell'

type Phase = 'ready' | 'subscribed' | 'everywhere' | 'gone'

const TITLE = { fontSize: 24, fontWeight: 700, letterSpacing: '-0.03em' } as const
const POP = { animation: 'blogPop 320ms var(--ease-out)' } as const

export function Confirm({ token, onHome, onSubscribe }: { token: string | null; onHome: () => void; onSubscribe: () => void }) {
  const [phase, setPhase] = useState<Phase>(token === null ? 'gone' : 'ready')
  const [sending, setSending] = useState(false)
  const [failed, setFailed] = useState(false)
  const inFlight = useRef(false)

  const confirm = async () => {
    if (token === null || inFlight.current) return
    inFlight.current = true
    setFailed(false)
    setSending(true)
    try {
      const { unsubscribedEverywhere } = await confirmSubscription(token)
      clearConfirmToken()
      setPhase(unsubscribedEverywhere ? 'everywhere' : 'subscribed')
    } catch (err) {
      if (err instanceof ApiError && err.status === 410) {
        clearConfirmToken()
        setPhase('gone')
      } else setFailed(true)
    } finally {
      inFlight.current = false
      setSending(false)
    }
  }

  const back = (
    <Button variant="primary" size="sm" onClick={onHome}>Back to the blog</Button>
  )

  return (
    <NewsletterShell>
      {phase === 'ready' && (
        <div style={COLUMN(16)}>
          <span style={TITLE}>Confirm your subscription.</span>
          <span className="t-body" style={{ color: 'var(--foreground)' }}>
            Click the button to start getting the posts you chose.
          </span>
          <div style={COLUMN(10)}>
            <Button variant="primary" arrow disabled={sending} onClick={confirm} style={{ width: '100%' }}>Confirm subscription</Button>
            {failed && (
              <span style={{ fontSize: '12.5px', fontWeight: 600, color: 'var(--destructive)' }}>
                We could not confirm right now. Try again in a minute.
              </span>
            )}
          </div>
        </div>
      )}
      {phase === 'subscribed' && (
        <div style={{ ...COLUMN(16), ...POP }}>
          <StatusTile />
          <span style={TITLE}>You are subscribed.</span>
          <span className="t-body" style={{ color: 'var(--foreground)' }}>
            Your topics and frequency are saved. Every email has a one-click unsubscribe.
          </span>
          <div style={{ marginTop: 6 }}>{back}</div>
        </div>
      )}
      {phase === 'everywhere' && (
        <div style={{ ...COLUMN(16), ...POP }}>
          <span style={TITLE}>Your topics are saved, but blog emails will not reach you.</span>
          <span className="t-body" style={{ color: 'var(--foreground)' }}>
            This address is unsubscribed from all ASComply email, so we cannot send you blog posts.
          </span>
          <div style={{ marginTop: 6 }}>{back}</div>
        </div>
      )}
      {phase === 'gone' && (
        <div style={COLUMN(16)}>
          <span style={TITLE}>This link has expired or was already used.</span>
          <span className="t-body" style={{ color: 'var(--foreground)' }}>
            Confirmation links work once and expire after 48 hours. If you already confirmed, you are subscribed.
          </span>
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginTop: 6 }}>
            <Button variant="primary" size="sm" onClick={onSubscribe}>Subscribe again</Button>
            <Button variant="outline" size="sm" onClick={onHome}>Back to the blog</Button>
          </div>
        </div>
      )}
    </NewsletterShell>
  )
}
