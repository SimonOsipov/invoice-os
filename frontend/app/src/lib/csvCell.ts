// Prefix a formula lead with ', then RFC 4180 quote. ' is in the set so the rule reverses.
export function csvCell(value: string): string {
  const v = /^[=+\-@\t\r']/.test(value) ? `'${value}` : value
  return /[",\r\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
}
