// Stub: signatures only. FLOWUPD-01-06 implements them.
export interface MeasureRequest {
  selector: string
  props: string[]
  viewport?: number
  session: string
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export interface MeasureResult {
  selector: string
  viewport: { width: number; height: number }
  layoutWidth: number
  count: number
  matches: { index: number; box: Box; styles: Record<string, string> }[]
}

// `args` are playwright-cli arguments, without the binary.
export interface ExecResult {
  code: number
  stdout: string
  stderr?: string
}
export type Exec = (args: string[]) => Promise<ExecResult>

export function parseMeasureArgs(_positionals: string[], _flags: Record<string, string | undefined>): MeasureRequest {
  return { selector: '', props: [], session: '' }
}

export function measureSnippet(_req: MeasureRequest): string {
  return 'async page => ({ stub: true })'
}

export function playwrightCliCommand(_args: string[]): { file: string; args: string[]; cwd: string } {
  return { file: '', args: [], cwd: '' }
}

export async function measure(_req: MeasureRequest, _exec?: Exec): Promise<MeasureResult> {
  return {} as MeasureResult
}
