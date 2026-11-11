import { useEffect, useState } from 'react'
import { Footer } from './components/Footer'
import { Header } from './components/Header'
import { Hero } from './components/Hero'
import { ProgressStrip } from './components/ProgressStrip'
import { chooseConsent, CookieNotice, readConsent, syncCheckConsent } from './analytics'
import { runCheck, suggestMapping } from './checkApi'
import { DetailsStep } from './components/DetailsStep'
import { MapStep } from './components/MapStep'
import { SentPanel } from './components/SentPanel'
import { UploadStep } from './components/UploadStep'
import { WhatYouGet } from './components/WhatYouGet'
import { canSend, fileVerdict, kindOf } from './flow'
import { initialMapping, type Mapping, type Suggestion } from './fields'
import { landingHref } from './links'
import { COLUMNS_SENTENCE, refusal as refuse, type Refusal } from './messages'
import { progress, type Step } from './progress'

export function App() {
  const [consent, setConsent] = useState(() => readConsent())
  const [reopened, setReopened] = useState(false)
  const [step, setStep] = useState<Step>('upload')
  const [file, setFile] = useState<File | null>(null)
  const [verdict, setVerdict] = useState('')
  const [refusal, setRefusal] = useState<Refusal | null>(null)
  const [reading, setReading] = useState(false)
  const [suggestion, setSuggestion] = useState<Suggestion | null>(null)
  const [fields, setFields] = useState<Mapping | null>(null)
  const [email, setEmail] = useState('')
  const [ticked, setTicked] = useState(false)
  const [sending, setSending] = useState(false)

  // A new pick drops everything the previous file left behind.
  const pick = async (picked: File) => {
    setStep('upload')
    setFile(null)
    setSuggestion(null)
    setFields(null)
    setRefusal(null)
    setTicked(false)
    const v = fileVerdict(picked)
    setVerdict(v)
    if (v) return
    if (kindOf(picked) !== 'spreadsheet') {
      setFile(picked)
      setStep('details')
      return
    }
    setFile(picked)
    setReading(true)
    const res = await suggestMapping(picked)
    setReading(false)
    if (res.ok && res.value.columns.length > 0) {
      setSuggestion(res.value)
      setFields(initialMapping(res.value))
      setStep('map')
      return
    }
    setFile(null)
    setRefusal(res.ok ? { text: COLUMNS_SENTENCE } : refuse('mapping', res.status, res.error))
  }

  const backToUpload = () => {
    setStep('upload')
    setFile(null)
    setSuggestion(null)
    setFields(null)
    setRefusal(null)
    setTicked(false)
    setVerdict('')
  }

  const send = async () => {
    if (!file) return
    setSending(true)
    setRefusal(null)
    const res = await runCheck({ email, file, mapping: suggestion && fields ? { headerRow: suggestion.header_row, fields } : undefined })
    setSending(false)
    if (res.ok) setStep('sent')
    else setRefusal(refuse(suggestion ? 'run' : 'document', res.status, res.error))
  }

  // A choice made in another tab applies here when this tab is shown again.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return
      const next = syncCheckConsent()
      if (next) setConsent(next)
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [])

  return (
    <div className="check-page">
      <Header />
      <main className="check-main">
        <Hero wide={step === 'map'}>
          <ProgressStrip progress={progress(step, file?.name ?? null)} />
          <div className="check-steps">
            {step === 'upload' && <UploadStep fileName={file?.name ?? null} reading={reading} verdict={verdict} refusal={refusal} onPick={pick} />}
            {step === 'map' && file && suggestion && fields && (
              <MapStep
                fileName={file.name}
                suggestion={suggestion}
                fields={fields}
                onField={(key, header) => setFields({ ...fields, [key]: header })}
                onBack={backToUpload}
                onContinue={() => setStep('details')}
              />
            )}
            {step === 'details' && file && (
              <DetailsStep
                fileName={file.name}
                email={email}
                ticked={ticked}
                sending={sending}
                canSend={canSend({ file, email, ticked, fields })}
                refusal={refusal}
                onEmail={setEmail}
                onTick={setTicked}
                onBack={() => {
                  setRefusal(null)
                  if (suggestion) setStep('map')
                  else backToUpload()
                }}
                onSend={send}
              />
            )}
            {step === 'sent' && <SentPanel email={email.trim()} />}
          </div>
        </Hero>
        <WhatYouGet />
      </main>
      <Footer onCookieChoices={() => setReopened(true)} />
      {(consent === null || reopened) && (
        <CookieNotice
          current={consent}
          suppressed={false}
          privacyHref={landingHref('/privacy')}
          onChoose={(c) => {
            setConsent(chooseConsent(c))
            setReopened(false)
          }}
        />
      )}
      <style>{__COOKIE_NOTICE_CSS__}</style>
    </div>
  )
}
