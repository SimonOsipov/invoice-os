import { useEffect } from 'react'
import { CONSENT_TEXT } from './demoForm'
import { PRODUCTION_HOSTNAMES } from '../hubspot'

export const GA_RETENTION_MONTHS = 14
export const PROSE_MAX_WIDTH = 720
export const PRIVACY_CONTACT = 'sam@ascomply.com'
export const ANALYTICS_DEFAULT_SENTENCE = 'Analytics is off unless you turn it on.'

const PAGE_TITLE = 'Privacy and cookies — ASComply Africa'

const H2 = { fontSize: 21, lineHeight: 1.3, letterSpacing: '-0.02em', fontWeight: 600, margin: '38px 0 12px' } as const
const P = { fontSize: 16, lineHeight: 1.65, color: 'var(--fg-2)', margin: '0 0 14px' } as const
const LIST = { ...P, paddingLeft: 22 } as const
const ITEM = { margin: '0 0 10px' } as const
// .asc-app a is `color: inherit; text-decoration: none`, so an unstyled link is invisible.
// overflowWrap lets the long gaoptout URL break instead of overhanging its column at 390px.
const LINK = { color: 'var(--action)', textDecoration: 'underline', overflowWrap: 'anywhere' } as const

export function Privacy() {
  // index.html carries one static title, written for the sales page.
  useEffect(() => {
    document.title = PAGE_TITLE
  }, [])

  return (
    <section style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div data-testid="privacy-container" style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div
          data-testid="privacy-prose"
          data-prose-max={PROSE_MAX_WIDTH}
          style={{ maxWidth: PROSE_MAX_WIDTH, margin: '0 auto' }}
        >
          <div className="eyebrow" style={{ marginBottom: 14 }}>
            PRIVACY &amp; COOKIES
          </div>
          <h1 style={{ fontSize: 38, lineHeight: 1.1, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 18px' }}>
            Privacy and cookies
          </h1>

          <p style={P}>
            This page explains what happens to information about you when you use this site. It is written in plain
            English rather than legal language, and it describes what the site does today — not what it might do later.
            It is not legal advice, and we do not claim here that it satisfies any particular data-protection law.
          </p>
          <p style={P}>
            Two other companies receive information about your visit. Google measures how this site is used and also
            serves the fonts it is typeset in. HubSpot stores the answers you give if you book a demo. Your browser
            loads nothing on this site from any other company.
          </p>

          <h2 style={H2}>Google Analytics</h2>
          <p style={P}>
            We use Google Analytics 4 to measure how people use this marketing site — which pages get read, what
            visitors do next, and where the site is confusing. It runs only if you have allowed analytics.
          </p>
          <p style={P}>
            It runs on this public site only. There is no analytics code anywhere inside the signed-in ASComply product.
          </p>
          <p style={P}>
            It is active on {PRODUCTION_HOSTNAMES[0]} and nowhere else. Our preview and test builds run the same code,
            but that code only measures on the live address, so those builds send Google nothing.
          </p>

          <h2 style={H2}>What Google receives</h2>
          <ul style={LIST}>
            <li style={ITEM}>which pages you viewed</li>
            <li style={ITEM}>where you arrived from</li>
            <li style={ITEM}>how far down a page you scrolled</li>
            <li style={ITEM}>when you opened the demo form</li>
            <li style={ITEM}>whether that form then succeeded or failed</li>
            <li style={ITEM}>your device, browser and operating system, your screen size and your language</li>
            <li style={ITEM}>a randomly generated identifier that lets Google recognise the same browser on a later visit</li>
          </ul>

          <h2 style={H2}>What Google never receives</h2>
          <p style={P}>
            We never send Google your name, your email address, your company, or any answer you typed or chose in the
            demo form. The only details we attach ourselves are which button you used, which form it was, and how far
            down you scrolled — the rest of the list above is collected by Google's own code.
          </p>

          <h2 style={H2}>Cookies on your device</h2>
          <p style={P}>
            Google Analytics sets two cookies on your device. One is named _ga; the other starts _ga_ and ends in a code
            specific to our analytics property. Together they are what let Google tell a returning visit from a new one.
          </p>
          <p style={P}>
            Our own code sets no cookies at all — these come from Google's script. They appear only once you have
            allowed analytics; until then your cookie list for this site holds neither of them.
          </p>

          <h2 style={H2}>Where the data goes</h2>
          <p style={P}>
            Your visit is collected through one of Google's regional collection endpoints — from this site,
            region1.google-analytics.com. Google LLC then processes and stores that data, including in the United
            States.
          </p>
          <p style={P}>
            Using this site therefore transfers information about your visit outside Nigeria and outside the EEA.
          </p>

          <h2 style={H2}>How long it is kept</h2>
          <p style={P}>
            Google deletes the underlying record of your visit after {GA_RETENTION_MONTHS} months.
          </p>

          <h2 style={H2}>What it is not used for</h2>
          <p style={P}>
            We have Google Signals turned off, so none of this is joined to Google advertising profiles or used to
            follow you between your devices. We run no advertising network on this site, and nothing here sets an
            advertising cookie.
          </p>

          <h2 style={H2}>Google Fonts</h2>
          <p style={P}>
            Google also serves the fonts this site is typeset in. Loading a font tells Google your IP address and which
            site asked for it.
          </p>
          <p style={P}>
            This happens on every page of this site, every time, whatever you decide about analytics. It is not behind
            the analytics switch and it is not behind any consent check — it is the one flow on this site with no gate
            in front of it, and none of the controls further down stops it.
          </p>

          <h2 style={H2}>If you book a demo</h2>
          <p style={P}>
            Booking a demo sends your answers to HubSpot, the customer-relationship system we use to keep track of who
            has asked for one.
          </p>
          <p style={P}>
            HubSpot receives exactly this: your first and last name — split from the single full name you typed — your
            work email, your company, and, unless you cleared them, your role, your taxpayer size and your monthly
            invoice volume. Anything left empty is not sent at all, neither as a blank value nor as an empty field.
          </p>
          <p style={P}>
            The three optional questions come with an answer already selected when the form opens, so unless you change
            them, the pre-selected answer is what gets sent.
          </p>
          <p style={P}>
            The sentence you tick before submitting is stored alongside your details, word for word: “{CONSENT_TEXT}”
            Ticking it does not add you to a marketing list — the record says only that you agreed to be contacted about
            this demo request.
          </p>
          <p style={P}>HubSpot holds all of this on their EU servers.</p>
          <p style={P}>
            We do not send HubSpot your browsing history, the pages you visited, or HubSpot's own tracking cookie. The
            form carries your answers and nothing else.
          </p>

          <h2 style={H2}>How to stop being measured</h2>
          <p style={P}>
            <strong>{ANALYTICS_DEFAULT_SENTENCE}</strong> This site has no privacy control of its own yet — no notice,
            no toggle, no settings page. One that lets you choose is being built. Until it ships, no analytics runs at
            all, and the four options below are what actually exist. We would rather say that plainly than point you
            at a control you cannot find.
          </p>
          <ol style={LIST}>
            <li style={ITEM}>
              <strong>Block or clear cookies for this site.</strong> Your browser's own site settings can delete the _ga
              cookies and stop new ones being issued. That breaks the thread between your visits: Google can no longer
              tell that today's visit and last week's came from the same browser. It does not stop the measurement
              itself — the Google Analytics code still runs, and Google still receives the page you are on and the IP
              address your request comes from.
            </li>
            <li style={ITEM}>
              <strong>Install Google's own opt-out add-on.</strong> Google publishes a browser add-on at{' '}
              <a href="https://tools.google.com/dlpage/gaoptout" className="ios-link" style={LINK}>
                https://tools.google.com/dlpage/gaoptout
              </a>{' '}
              that stops Google Analytics from sending anything, on this site and on every other site that uses it.
              Google lists it for Chrome, Firefox, Safari and Microsoft Edge on the desktop; there is no version for a
              phone browser. It does not affect the fonts and it does not affect the demo form.
            </li>
            <li style={ITEM}>
              <strong>Block googletagmanager.com with a content blocker.</strong> If that host never loads, the
              analytics code never arrives and nothing is sent. This is the most complete of the three browser
              controls: it stops the
              measurement before it starts. It does not stop the fonts, which come from a different Google host, and it
              does not stop the demo form.
            </li>
            <li style={ITEM}>
              <strong>Write to us.</strong> Email{' '}
              <a href={`mailto:${PRIVACY_CONTACT}`} className="ios-link" style={LINK}>
                {PRIVACY_CONTACT}
              </a>{' '}
              and tell us what you want done, and we will act on it. If you have booked a demo, the record we hold is in
              HubSpot and we can delete it. Analytics data sits with Google under an identifier that is not tied to your
              name, so tell us as much as you can about when you visited.
            </li>
          </ol>
          <p style={P}>
            None of the four stops Google Fonts. That request goes out on every page of this site whatever else you do;
            only a content blocker aimed at fonts.googleapis.com will stop it.
          </p>

          <h2 style={H2}>Getting in touch</h2>
          <p style={P}>
            For anything on this page — a question, a correction, or a request about your own data — email{' '}
            <a href={`mailto:${PRIVACY_CONTACT}`} className="ios-link" style={LINK}>
              {PRIVACY_CONTACT}
            </a>
            . A person at ASComply Africa reads that address.
          </p>
        </div>
      </div>
    </section>
  )
}
