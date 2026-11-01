import { test, expect } from '@playwright/test'

import { provisionRealAccount, rawFetch } from '../api/client'
import { requireEnv, writeAccounts } from './mailChecks'

// Runs before the mail is turned on: auto-confirm is on, so both accounts sign in at once.
test('prepare: the admin and the reset account exist', async () => {
  const env = requireEnv('NIGHTLY_RUN_ID', 'RESEND_RECEIVING_DOMAIN', 'MAIL_ACCOUNTS_FILE')
  const run = env.NIGHTLY_RUN_ID
  const admin = await provisionRealAccount('mail-admin', 'firm', 'Mail Admin', `Mail Workspace ${run}`)
  const reset = { email: `nightly-${run}-reset@${env.RESEND_RECEIVING_DOMAIN}`, password: crypto.randomUUID().slice(0, 16) }
  const res = await rawFetch('/auth/register', { method: 'POST', body: reset })
  expect(res.status, 'the reset account registration').toBe(202)
  writeAccounts(env.MAIL_ACCOUNTS_FILE, { admin, reset })
})
