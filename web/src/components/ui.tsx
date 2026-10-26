import { useEffect, useRef, type ReactNode } from 'react'
import { X } from 'lucide-react'
import type { Health } from '../api'

/** The 5a owl: blue ear tufts, amber-ringed eyes with heptagon pupils, beak. */
export function OwlMark({ size = 32 }: { size?: number }) {
  const hept = (cx: number, cy: number, r: number) =>
    Array.from({ length: 7 }, (_, i) => {
      const a = ((-90 + (i * 360) / 7) * Math.PI) / 180
      return `${(cx + r * Math.cos(a)).toFixed(1)},${(cy + r * Math.sin(a)).toFixed(1)}`
    }).join(' ')
  const eye = (x: number) => (
    <g key={x}>
      <circle cx={x} cy={500} r={136} fill="#FBBF24" />
      <circle cx={x} cy={500} r={112} fill="#FFFFFF" />
      <polygon points={hept(x, 508, 62)} fill="#326CE5" />
      <circle cx={x - 24} cy={476} r={16} fill="#FFFFFF" />
    </g>
  )
  return (
    <svg width={size} height={size} viewBox="230 240 564 470" aria-hidden="true">
      <path d="M 254 250 L 416 360 L 294 420 Z" fill="#326CE5" />
      <path d="M 770 250 L 608 360 L 730 420 Z" fill="#326CE5" />
      {eye(380)}
      {eye(644)}
      <path d="M 484 626 L 540 626 L 512 684 Z" fill="#F59E0B" />
    </svg>
  )
}

const healthColor: Record<Health, string> = {
  healthy: 'var(--color-ok)',
  degraded: 'var(--color-warn-soft)',
  down: 'var(--color-bad)',
}

export const healthText: Record<Health, string> = { healthy: 'Healthy', degraded: 'Degraded', down: 'Down' }

/** The owl's heartbeat, drawn once: a steady beat, a ragged one, or a flat line. */
export function Pulse({ health }: { health: Health }) {
  const d =
    health === 'healthy'
      ? 'M0 30 H150 L170 30 L185 8 L205 46 L220 18 L232 30 H420 L440 30 L455 8 L475 46 L490 18 L502 30 H600'
      : health === 'degraded'
        ? 'M0 30 H90 L104 22 L118 38 L132 26 L146 30 H250 L262 14 L278 42 L290 24 L300 30 H420 L430 26 L442 36 L452 30 H600'
        : 'M0 30 H600'
  return (
    <svg key={health} viewBox="0 0 600 48" preserveAspectRatio="none" className="pulse-line h-10 w-full" aria-hidden="true">
      <path
        d={d}
        fill="none"
        stroke={healthColor[health]}
        strokeWidth={3}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}

export function HealthDot({ health }: { health: Health }) {
  return (
    <span
      className="inline-block size-2.5 shrink-0 rounded-full"
      style={{ background: healthColor[health] }}
      aria-label={healthText[health]}
      role="img"
    />
  )
}

export function Pill({ tone, children }: { tone: 'ok' | 'warn' | 'bad' | 'plain'; children: ReactNode }) {
  const styles = {
    ok: 'text-ok bg-ok/10',
    warn: 'text-warn bg-warn-soft/15',
    bad: 'text-bad bg-bad/10',
    plain: 'text-muted bg-sunken',
  }[tone]
  return <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${styles}`}>{children}</span>
}

/** Side panel for logs, JSON and pickers. Closes on Esc or backdrop click. */
export function Panel({
  title,
  subtitle,
  onClose,
  children,
  wide = true,
}: {
  title: string
  subtitle?: string
  onClose: () => void
  children: ReactNode
  wide?: boolean
}) {
  const closeRef = useRef<HTMLButtonElement>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose
  useEffect(() => {
    closeRef.current?.focus() // only when the panel opens
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onCloseRef.current()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true" aria-label={title}>
      <button className="absolute inset-0 cursor-default bg-ink/30" aria-label="Close panel" tabIndex={-1} onClick={onClose} />
      <section
        className={`relative flex h-full w-full flex-col border-l border-line bg-surface shadow-2xl ${wide ? 'max-w-3xl' : 'max-w-md'}`}
      >
        <header className="flex items-start gap-3 border-b border-line px-5 py-4">
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-lg font-semibold">{title}</h2>
            {subtitle && <p className="truncate text-sm text-muted">{subtitle}</p>}
          </div>
          <button
            ref={closeRef}
            onClick={onClose}
            className="rounded-md p-1.5 text-muted hover:bg-sunken hover:text-ink"
            aria-label="Close"
          >
            <X size={20} />
          </button>
        </header>
        <div className="min-h-0 flex-1 overflow-auto">{children}</div>
      </section>
    </div>
  )
}

export function Loading({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 p-6 text-sm text-muted" role="status">
      <span className="size-4 animate-spin rounded-full border-2 border-line border-t-blue" />
      {label}
    </div>
  )
}

export function ErrorNote({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return (
    <div className="m-4 flex flex-wrap items-center gap-3 rounded-lg bg-bad/10 px-4 py-3 text-sm text-bad" role="alert">
      <span className="flex-1">{error instanceof Error ? error.message : String(error)}</span>
      {onRetry && (
        <button onClick={onRetry} className="font-medium underline underline-offset-2">
          Try again
        </button>
      )}
    </div>
  )
}

export const inputClass =
  'h-9 rounded-md border border-line bg-surface px-3 text-sm text-ink placeholder:text-muted focus:border-blue focus:outline-none'
