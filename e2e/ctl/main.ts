// Stub: exports exist, behaviour is wrong, so the Mode A reds fail on assertions (FLOWUPD-01 D39).
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

export const HELP: Record<'ctl' | 'env' | 'login' | 'measure', string> = {
  ctl: '',
  env: '',
  login: '',
  measure: '',
}

export async function run(
  _argv: string[],
  _commands?: Partial<Commands>,
): Promise<{ code: 0 | 1 | 2; stdout: string; stderr: string }> {
  return { code: 2, stdout: '', stderr: '{}' }
}
