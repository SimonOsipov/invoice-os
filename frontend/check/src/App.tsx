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
import { CHECK_FIELD_KEYS, formatBytes, initialMapping, type Mapping, type Suggestion } from './fields'
import { EMAIL_STEP_NO, FILE_MATCHED, FILE_NO_MATCHING } from './copy'
import { landingHref } from './links'
import { COLUMNS_SENTENCE, MAPPING_SENTENCE, refusal as refuse, type Refusal } from './messages'
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
    if (res.ok) {
      setStep('sent')
      return
    }
    const r = refuse(suggestion ? 'run' : 'document', res.status, res.error)
    setRefusal(r)
    // Only a rejected mapping goes back to step 2; every other refusal stays on the email step.
    if (suggestion && r.text === MAPPING_SENTENCE) setStep('map')
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
            {step === 'upload' && <UploadStep file={file ? { name: file.name, size: file.size } : null} reading={reading} verdict={verdict} refusal={refusal} onPick={pick} />}
            {step === 'map' && file && suggestion && fields && (
              <MapStep
                fileName={file.name}
                suggestion={suggestion}
                fields={fields}
                error={refusal?.text ?? null}
                onField={(key, header) => {
                  setRefusal(null)
                  setFields({ ...fields, [key]: header })
                }}
                onBack={backToUpload}
                onContinue={() => {
                  setRefusal(null)
                  setStep('details')
                }}
              />
            )}
            {step === 'details' && file && (
              <DetailsStep
                stepNo={EMAIL_STEP_NO(kindOf(file) === 'spreadsheet')}
                fileName={file.name}
                fileMeta={`${formatBytes(file.size)} · ${fields ? FILE_MATCHED(CHECK_FIELD_KEYS.filter((k) => fields[k] != null).length, CHECK_FIELD_KEYS.length) : FILE_NO_MATCHING}`}
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
