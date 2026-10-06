import { parseArgs } from 'node:util'

export class CtlError extends Error {
  hint: string
  code: 1 | 2
  extra?: Record<string, unknown>

  constructor(message: string, hint: string, code: 1 | 2, extra?: Record<string, unknown>) {
    super(message)
    this.hint = hint
    this.code = code
    this.extra = extra
  }
}

type Command = (positionals: string[], flags: Record<string, string | undefined>) => Promise<unknown>

export interface Commands {
  env: Command
  login: Command
  measure: Command
}

const CTL = 'pnpm -s --filter @invoice-os/e2e ctl'

export const HELP: Record<'ctl' | 'env' | 'login' | 'measure', string> = {
  ctl: `Usage: ctl <command> [options]

Commands:
  env      Resolve the five service URLs of a Railway environment and report dark domains
  login    Sign in as a demo persona on a PR environment and save a browser storage state
  measure  Report the box and computed styles of every element matching a selector

Every command prints one JSON object: on stdout with exit 0, or on stderr with exit 1 (failure) or 2 (usage).
Run "ctl <command> --help" for one command.
Example: ${CTL} env pr-348`,
  env: `Usage: ctl env <pr-N|production>

Prints { env, environmentId, urls, dark }. urls holds GATEWAY_URL, APP_URL, LANDING_URL,
OPS_CONSOLE_URL and SUPPORT_CONSOLE_URL. A dark domain exits 1 and is listed in dark.
Needs a Railway CLI login (~/.railway/config.json).
Example: ${CTL} env pr-348`,
  login: `Usage: ctl login <firm|inhouse|developer|support> --env <pr-N> [--role admin|preparer|reviewer] [--session S]

Provisions the demo accounts once per environment, signs in, and saves a storage state.
--role applies to firm and inhouse only (default admin). --env production is refused.
--session defaults to $PLAYWRIGHT_CLI_SESSION, else "default".
Prints { env, persona, role, email, tenantId, url, storageState, created, reused, gatewayWrites, next }.
Example: ${CTL} login firm --env pr-348 --role reviewer`,
  measure: `Usage: ctl measure <selector> --props <p1,p2,...> [--viewport <width>] [--session S]

Prints the layout box and the listed computed styles of every match in the open playwright-cli session.
--viewport resizes the page to that width first.
Prints { selector, viewport, layoutWidth, count, matches }.
Example: ${CTL} measure '[data-testid="evidence-bundle-drawer"]' --props width,padding-left --viewport 1440`,
}

const notBuilt =
  (name: string): Command =>
  async () => {
    throw new CtlError(`${name} is not built yet`, `Run ${CTL} --help for the commands that work.`, 1)
  }

// Lazy so --help loads no command module.
const DEFAULTS: Commands = {
  env: async (p, f) => (await import('./railway')).envCommand(p, f),
  login: notBuilt('login'),
  measure: notBuilt('measure'),
}

const json = (value: unknown) => JSON.stringify(value) + '\n'

const fail = (code: 1 | 2, error: string, hint: string, extra?: Record<string, unknown>) => ({
  code,
  stdout: '',
  stderr: json({ error, hint, ...extra }),
})

export async function run(
  argv: string[],
  commands: Partial<Commands> = {},
): Promise<{ code: 0 | 1 | 2; stdout: string; stderr: string }> {
  const hint = `Run ${CTL} --help`
  let parsed
  try {
    parsed = parseArgs({
      args: argv,
      allowPositionals: true,
      options: {
        env: { type: 'string' },
        role: { type: 'string' },
        props: { type: 'string' },
        viewport: { type: 'string' },
        session: { type: 'string' },
        help: { type: 'boolean' },
      },
    })
  } catch (err) {
    return fail(2, err instanceof Error ? err.message : String(err), hint)
  }
  const { positionals, values: { help, ...flags } } = parsed
  const [name, ...rest] = positionals
  if (name === undefined) {
    if (help) return { code: 0, stdout: HELP.ctl + '\n', stderr: '' }
    return fail(2, 'no command given', hint)
  }
  if (name !== 'env' && name !== 'login' && name !== 'measure') {
    return fail(2, `unknown command: ${name}`, hint)
  }
  if (help) return { code: 0, stdout: HELP[name] + '\n', stderr: '' }
  try {
    const result = await { ...DEFAULTS, ...commands }[name](rest, flags)
    return { code: 0, stdout: json(result), stderr: '' }
  } catch (err) {
    if (err instanceof CtlError) return fail(err.code, err.message, err.hint, err.extra)
    return fail(1, err instanceof Error ? err.message : String(err), `${hint}, and "ctl ${name} --help" for its usage.`)
  }
}

if (import.meta.main) {
  // No top-level await: railway.ts imports CtlError from this module and would deadlock on it.
  void run(process.argv.slice(2)).then((r) => {
    process.stdout.write(r.stdout)
    process.stderr.write(r.stderr)
    process.exitCode = r.code
  })
}
