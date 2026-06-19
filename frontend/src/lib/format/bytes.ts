/**
 * formatBytes — pick a sensible unit for a byte count and return
 * `{ value, unit }` separately so the caller can style the unit
 * differently from the number (e.g. smaller weight, muted colour).
 *
 * Uses binary (1024-based) units, which is what the storage layer
 * reports and what users expect for container images. Switching to
 * decimal (1000-based) units would understate disk usage compared to
 * what `du -h` shows in the container runtime.
 */
const KIB = 1024
const MIB = KIB * 1024
const GIB = MIB * 1024
const TIB = GIB * 1024

export interface FormattedBytes {
  /** Numeric value in the chosen unit, rounded to one decimal. */
  value: number
  /** Unit string — `B`, `KB`, `MB`, `GB`, or `TB`. */
  unit: string
}

export function formatBytes(bytes: number): FormattedBytes {
  if (bytes >= TIB) return { value: round1(bytes / TIB), unit: 'TB' }
  if (bytes >= GIB) return { value: round1(bytes / GIB), unit: 'GB' }
  if (bytes >= MIB) return { value: round1(bytes / MIB), unit: 'MB' }
  if (bytes >= KIB) return { value: round1(bytes / KIB), unit: 'KB' }
  return { value: bytes, unit: 'B' }
}

/** Round to 1 decimal place; trims `.0` if the value is whole. */
function round1(n: number): number {
  return Math.round(n * 10) / 10
}
