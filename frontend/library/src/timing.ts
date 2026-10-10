export const STEP_SECONDS = 3.4

export function formatSeconds(s: number): string {
  const t = Math.max(0, Math.floor(s))
  return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, '0')}`
}

// Step shown `seconds` into a demo of `steps` steps; clamps to the last step.
export function stepAt(seconds: number, steps: number): number {
  return Math.min(steps - 1, Math.max(0, Math.floor(seconds / STEP_SECONDS)))
}
