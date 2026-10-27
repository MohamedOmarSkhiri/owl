// Small, dependency-free SVG charts for the Overview: a line chart with a hover readout,
// and a sparkline. Colours come from CSS tokens (--color-series-*), so both themes work.
import { useEffect, useRef, useState } from 'react'

export interface Series {
  name: string
  color: string // a CSS colour, e.g. 'var(--color-series-1)'
  values: (number | null)[] // one per time; null = no data (drawn as a gap)
}

/** Width of an element, kept up to date when the window or the layout changes. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(0)
  useEffect(() => {
    if (!ref.current) return
    const ro = new ResizeObserver(([e]) => setWidth(e.contentRect.width))
    ro.observe(ref.current)
    return () => ro.disconnect()
  }, [])
  return [ref, width] as const
}

/** SVG path through the points, broken where a value is null. */
function linePath(values: (number | null)[], x: (i: number) => number, y: (v: number) => number) {
  let d = ''
  let pen = false
  values.forEach((v, i) => {
    if (v === null) {
      pen = false
      return
    }
    d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`
    pen = true
  })
  return d
}

const hhmm = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

/**
 * Percent line chart (0–100 %). One y axis only. A legend names the series, each line also
 * carries its name and latest value at its end, and hovering shows every series at that time.
 */
export function LineChart({ times, series, label }: { times: number[]; series: Series[]; label: string }) {
  const [wrap, width] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<number | null>(null)
  const H = 220
  const m = { top: 12, right: 92, bottom: 26, left: 40 }
  const w = Math.max(0, width - m.left - m.right)
  const h = H - m.top - m.bottom
  const n = times.length
  const x = (i: number) => m.left + (n > 1 ? (i / (n - 1)) * w : w / 2)
  const y = (v: number) => m.top + h - (Math.min(v, 100) / 100) * h

  // Direct labels at the end of each line, nudged apart when they would overlap.
  const ends = series
    .map((s) => {
      let i = s.values.length - 1
      while (i >= 0 && s.values[i] === null) i--
      return i < 0 ? null : { s, v: s.values[i]!, y: y(s.values[i]!) }
    })
    .filter((e) => e !== null)
    .sort((a, b) => a.y - b.y)
  for (let k = 1; k < ends.length; k++) if (ends[k].y - ends[k - 1].y < 16) ends[k].y = ends[k - 1].y + 16

  // Pointer → nearest point in time.
  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    if (n === 0) return
    const px = e.clientX - e.currentTarget.getBoundingClientRect().left
    setHover(Math.round(Math.min(Math.max((px - m.left) / (w || 1), 0), 1) * (n - 1)))
  }
  const onKey = (e: React.KeyboardEvent) => {
    if (n === 0) return
    if (e.key === 'ArrowLeft') setHover((i) => Math.max(0, (i ?? n) - 1))
    if (e.key === 'ArrowRight') setHover((i) => Math.min(n - 1, (i ?? -1) + 1))
  }

  const ticks = n > 1 ? [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(f * (n - 1))) : []

  return (
    <div>
      <ul className="mb-2 flex flex-wrap gap-4 text-xs text-muted" aria-label="Legend">
        {series.map((s) => (
          <li key={s.name} className="flex items-center gap-1.5">
            <span className="inline-block h-0.5 w-4 rounded" style={{ background: s.color }} aria-hidden />
            {s.name}
          </li>
        ))}
      </ul>
      <div ref={wrap} className="relative">
        {width > 0 && (
          <svg
            width={width}
            height={H}
            role="img"
            aria-label={label}
            tabIndex={0}
            onPointerMove={onMove}
            onPointerLeave={() => setHover(null)}
            onKeyDown={onKey}
            onBlur={() => setHover(null)}
            className="block touch-none outline-none focus-visible:ring-2 focus-visible:ring-blue"
          >
            {/* Recessive grid: 0, 50 and 100 % */}
            {[0, 50, 100].map((g) => (
              <g key={g}>
                <line x1={m.left} x2={m.left + w} y1={y(g)} y2={y(g)} stroke="var(--color-line)" strokeWidth={1} />
                <text x={m.left - 8} y={y(g) + 4} textAnchor="end" className="fill-muted text-[11px] tabular-nums">
                  {g}%
                </text>
              </g>
            ))}
            {ticks.map((i) => (
              <text key={i} x={x(i)} y={H - 6} textAnchor="middle" className="fill-muted text-[11px] tabular-nums">
                {hhmm(times[i])}
              </text>
            ))}
            {series.map((s) => (
              <path
                key={s.name}
                d={linePath(s.values, x, y)}
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ))}
            {ends.map((e) => (
              <text key={e.s.name} x={m.left + w + 8} y={e.y + 4} className="fill-ink text-[11px]">
                <tspan className="font-medium tabular-nums">{e.v}%</tspan>
                <tspan className="fill-muted"> {e.s.name}</tspan>
              </text>
            ))}
            {hover !== null && (
              <g>
                <line x1={x(hover)} x2={x(hover)} y1={m.top} y2={m.top + h} stroke="var(--color-muted)" strokeWidth={1} />
                {series.map((s) =>
                  s.values[hover] === null ? null : (
                    <circle
                      key={s.name}
                      cx={x(hover)}
                      cy={y(s.values[hover]!)}
                      r={4}
                      fill={s.color}
                      stroke="var(--color-surface)"
                      strokeWidth={2}
                    />
                  ),
                )}
              </g>
            )}
          </svg>
        )}
        {hover !== null && width > 0 && (
          <div
            className="pointer-events-none absolute top-1 rounded-md border border-line bg-surface px-3 py-2 text-xs shadow-lg"
            style={x(hover) > width / 2 ? { right: width - x(hover) + 12 } : { left: x(hover) + 12 }}
            role="status"
          >
            <div className="mb-1 text-muted tabular-nums">{hhmm(times[hover])}</div>
            {series.map((s) => (
              <div key={s.name} className="flex items-center gap-2">
                <span className="inline-block h-0.5 w-3 rounded" style={{ background: s.color }} aria-hidden />
                <span className="font-semibold tabular-nums">{s.values[hover] === null ? '—' : `${s.values[hover]}%`}</span>
                <span className="text-muted">{s.name}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** A tiny trend line for a stat card. No axes: the card's number gives the scale. */
export function Sparkline({ values, color, max }: { values: (number | null)[]; color: string; max?: number }) {
  const [wrap, width] = useWidth<HTMLDivElement>()
  const H = 32
  const known = values.filter((v) => v !== null)
  const top = max ?? Math.max(1, ...known)
  const x = (i: number) => (values.length > 1 ? (i / (values.length - 1)) * (width - 2) + 1 : width / 2)
  const y = (v: number) => H - 2 - (Math.min(v, top) / top) * (H - 4)
  return (
    <div ref={wrap} className="mt-2 h-8" aria-hidden>
      {width > 0 && known.length > 1 && (
        <svg width={width} height={H} className="block">
          <path d={linePath(values, x, y)} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
        </svg>
      )}
    </div>
  )
}