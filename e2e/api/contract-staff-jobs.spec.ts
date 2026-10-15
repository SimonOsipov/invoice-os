// The staff Submissions contract over the wire: list and detail match each other, refusals hold, and a
// customer token is refused on all four paths. Writes nothing: the 409 and 400 bodies are refused before any change.
import { test, expect } from '@playwright/test'
import { login, provisionStaffAccount, rawFetch, signInSession, PERSONAS } from './client'
import { assertErrorEnvelope } from './contract-helpers'

const JOBS = '/api/submission/v1/staff/jobs'
const DEMO_2222_ID = '22222222-2222-2222-2222-222222222222'

// maxAttempts in internal/staffjobs/handlers.go (submission.SubmitArgs InsertOpts, worker.go).
const MAX_ATTEMPTS = 8
// RedriveHandler: the ErrJobState arm, internal/staffjobs/handlers.go.
const REDRIVE_STATE_MESSAGE = 'only a dead_lettered job can be re-driven'
// readReason with maxReasonRunes = 500, internal/staffjobs/handlers.go.
const REASON_LENGTH_MESSAGE = 'reason must be 1 to 500 characters'

// Terminal seeded jobs of tenant 2222 (db/seed.dev.sql job_seed); no spec moves 7001..7003.
const SEEDED = { accepted: 'DEMO-2026-7001', rejected: 'DEMO-2026-7002', deferred: 'DEMO-2026-7003', deadA: 'DEMO-2026-7004', deadB: 'DEMO-2026-7006' }

type Job = { id: string; tenant_id: string; invoice_number: string; state: string; max_attempts: number; invoice_status: string }
type Step = { from_status: string | null; to_status: string; actor: string; changed_at: string }

test.describe('staff jobs (API E2E, over the deployed gateway)', () => {
  let customer: string
  let staff: string
  let jobs: Job[]

  const auth = (token: string) => ({ Authorization: `Bearer ${token}` })
  const byNumber = (n: string): Job => {
    const job = jobs.find((j) => j.invoice_number === n)
    if (!job) throw new Error(`seeded job ${n} is not in the tenant-2222 list; numbers: ${jobs.map((j) => j.invoice_number).join(', ')}`)
    return job
  }

  test.beforeAll(async () => {
    customer = await login(PERSONAS.A)
    const granted = await provisionStaffAccount('staff-jobs')
    staff = (await signInSession(granted.email, granted.password)).access_token
    const res = await rawFetch(`${JOBS}?tenant=${DEMO_2222_ID}`, { headers: auth(staff) })
    expect(res.status, 'staff jobs list').toBe(200)
    jobs = (res.body as { jobs: Job[] }).jobs
  })

  test('staff jobs: list and detail match a live read; refusals hold', async () => {
    // ceiling: the list is the newest 200 per filter; read the seeded rows by state if 2222 outgrows it.
    for (const n of Object.values(SEEDED)) byNumber(n)
    for (const j of jobs) expect(j.tenant_id, 'the tenant filter holds').toBe(DEMO_2222_ID)

    const accepted = byNumber(SEEDED.accepted)
    expect(accepted.state).toBe('accepted')
    expect(byNumber(SEEDED.rejected).state).toBe('rejected')
    expect(byNumber(SEEDED.deferred).state).toBe('accepted')
    for (const j of jobs) expect(j.max_attempts, `${j.invoice_number} max_attempts`).toBe(MAX_ATTEMPTS)

    const detail = await rawFetch(`${JOBS}/${accepted.id}`, { headers: auth(staff) })
    expect(detail.status, 'staff job detail').toBe(200)
    const body = detail.body as { job: Job; history: Step[] }
    expect(body.job, 'the detail job equals the listed job').toEqual(accepted)
    expect(body.job.max_attempts).toBe(MAX_ATTEMPTS)
    expect(body.history.length, 'the invoice history is not empty').toBeGreaterThan(0)
    expect(body.history[body.history.length - 1].to_status, 'history runs oldest first, ending at the invoice status').toBe(accepted.invoice_status)

    const redrive = await rawFetch(`${JOBS}/${accepted.id}/redrive`, { method: 'POST', headers: auth(staff), body: { reason: 'e2e refusal probe' } })
    assertErrorEnvelope(redrive, 409, 're-drive of an accepted job')
    expect((redrive.body as { error: string }).error).toBe(REDRIVE_STATE_MESSAGE)

    const empty = await rawFetch(`${JOBS}/${accepted.id}/redrive`, { method: 'POST', headers: auth(staff), body: { reason: '' } })
    assertErrorEnvelope(empty, 400, 're-drive with an empty reason')
    expect((empty.body as { error: string }).error).toBe(REASON_LENGTH_MESSAGE)

    const after = await rawFetch(`${JOBS}/${accepted.id}`, { headers: auth(staff) })
    expect((after.body as { job: Job }).job.state, 'the refused calls left the job accepted').toBe('accepted')

    const probe = { reason: 'e2e refusal probe' }
    assertErrorEnvelope(await rawFetch(JOBS, { headers: auth(customer) }), 403, 'customer list')
    assertErrorEnvelope(await rawFetch(`${JOBS}/${accepted.id}`, { headers: auth(customer) }), 403, 'customer detail')
    assertErrorEnvelope(await rawFetch(`${JOBS}/${accepted.id}/redrive`, { method: 'POST', headers: auth(customer), body: probe }), 403, 'customer re-drive')
    assertErrorEnvelope(await rawFetch(`${JOBS}/${accepted.id}/cancel`, { method: 'POST', headers: auth(customer), body: probe }), 403, 'customer cancel')
  })
})
