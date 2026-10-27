// Overview: the 10-second answer to "is this cluster OK, and what changed?".
// History comes from the relay (last 6 h in memory); the rest reuses the other tabs' queries.
import { useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, fmtCPU, fmtMem, percent, PODS, timeAgo, type Cluster, type PodUsage } from '../api'
import { LineChart, Sparkline } from './charts'
import { PodPanel } from './panels'
import { ErrorNote, Loading } from './ui'

const REFRESH = 15_000 // agents report every 15 s
const RANGES = [
  { minutes: 60, label: '1 h' },
  { minutes: 360, label: '6 h' },
]
const CPU = 'var(--color-series-1)'
const MEM = 'var(--color-series-2)'

function Card({ title, children, className = '' }: { title: string; children: ReactNode; className?: string }) {
  return (
    <section className={`rounded-xl border border-line bg-surface p-4 ${className}`}>
      <h2 className="text-sm font-medium text-muted">{title}</h2>
      {children}
    </section>
  )
}

function Stat({ title, value, sub, spark }: { title: string; value: string; sub?: string; spark: ReactNode }) {
  return (
    <Card title={title}>
      <p className="mt-1 text-3xl font-semibold tracking-tight tabular-nums">{value}</p>
      {sub && <p className="text-xs text-muted">{sub}</p>}
      {spark}
    </Card>
  )
}

export function OverviewTab({ cluster }: { cluster: Cluster }) {
  const id = cluster.id
  const navigate = useNavigate()
  const [minutes, setMinutes] = useState(60)
  const [openPod, setOpenPod] = useState<PodUsage | null>(null)

  // Same query keys as the Usage, Pods and Problems tabs: switching tabs reuses the data.
  const history = useQuery({ queryKey: ['history', id, minutes], queryFn: () => api.history(id, minutes), refetchInterval: REFRESH })
  const usage = useQuery({ queryKey: ['usage', id, 'pods', ''], queryFn: () => api.usage(id, 'pods'), refetchInterval: REFRESH })
  const pods = useQuery({ queryKey: ['list', id, 'pods', ''], queryFn: () => api.list(id, PODS), refetchInterval: REFRESH })
  const problems = useQuery({ queryKey: ['problems', id], queryFn: () => api.problems(id), refetchInterval: REFRESH })

  const pts = history.data ?? []
  const times = pts.map((p) => new Date(p.ts).getTime())
  const cpuLine = pts.map((p) => percent(p.usage?.cpuUsage, p.usage?.cpuAllocatable))
  const memLine = pts.map((p) => percent(p.usage?.memUsage, p.usage?.memAllocatable))
  const hasMetrics = cpuLine.some((v) => v !== null) || !!cluster.usage

  const u = cluster.usage
  const active = cluster.podsTotal - (cluster.podsCompleted ?? 0)

  // Top 5 pods by CPU and by memory.
  const top = (key: 'cpuUsage' | 'memUsage') =>
    [...(usage.data?.pods ?? [])].sort((a, b) => b[key] - a[key]).slice(0, 5).filter((p) => p[key] > 0)

  // Pods per namespace, biggest first; the rest folded into "Other".
  const perNs = useMemo(() => {
    const m = new Map<string, { total: number; bad: number }>()
    for (const p of pods.data ?? []) {
      const ns = p.namespace ?? ''
      const e = m.get(ns) ?? { total: 0, bad: 0 }
      e.total++
      if (!p.healthy) e.bad++
      m.set(ns, e)
    }
    const rows = [...m.entries()].map(([ns, e]) => ({ ns, ...e })).sort((a, b) => b.total - a.total)
    if (rows.length <= 8) return rows
    const rest = rows.slice(7)
    return [
      ...rows.slice(0, 7),
      { ns: `${rest.length} others`, total: rest.reduce((t, r) => t + r.total, 0), bad: rest.reduce((t, r) => t + r.bad, 0) },
    ]
  }, [pods.data])
  const nsMax = Math.max(1, ...perNs.map((r) => r.total))

  return (
    <div className="space-y-4 p-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Overview</h2>
        <div className="flex rounded-md border border-line p-0.5" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button
              key={r.minutes}
              aria-pressed={minutes === r.minutes}
              onClick={() => setMinutes(r.minutes)}
              className={`rounded px-3 py-1 text-sm ${minutes === r.minutes ? 'bg-blue text-white' : 'text-muted hover:text-ink'}`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat
          title="CPU used"
          value={u ? `${percent(u.cpuUsage, u.cpuAllocatable)}%` : '—'}
          sub={u ? `${fmtCPU(u.cpuUsage)} of ${fmtCPU(u.cpuAllocatable)}` : 'needs metrics-server'}
          spark={<Sparkline values={cpuLine} color={CPU} max={100} />}
        />
        <Stat
          title="Memory used"
          value={u ? `${percent(u.memUsage, u.memAllocatable)}%` : '—'}
          sub={u ? `${fmtMem(u.memUsage)} of ${fmtMem(u.memAllocatable)}` : 'needs metrics-server'}
          spark={<Sparkline values={memLine} color={MEM} max={100} />}
        />
        <Stat
          title="Pods running"
          value={`${cluster.podsRunning}/${active}`}
          sub={cluster.podsCompleted ? `${cluster.podsCompleted} completed` : undefined}
          spark={<Sparkline values={pts.map((p) => p.running)} color="var(--color-muted)" />}
        />
        <Stat
          title="Open problems"
          value={String(cluster.problemsOpen ?? 0)}
          sub={cluster.podsFailing ? `${cluster.podsFailing} pods failing` : 'no failing pods'}
          spark={<Sparkline values={pts.map((p) => p.problems)} color="var(--color-muted)" />}
        />
      </div>

      <Card title={`CPU and memory, % of capacity, last ${minutes === 60 ? 'hour' : '6 hours'}`}>
        <div className="mt-3">
          {history.isPending ? (
            <Loading />
          ) : history.isError ? (
            <ErrorNote error={history.error} onRetry={() => history.refetch()} />
          ) : !hasMetrics ? (
            <p className="py-8 text-center text-sm text-muted">
              No CPU and memory data: metrics-server is not installed in this cluster.
            </p>
          ) : pts.length < 2 ? (
            <p className="py-8 text-center text-sm text-muted">Collecting data. The chart fills in as the agent reports, every 15 s.</p>
          ) : (
            <LineChart
              times={times}
              series={[
                { name: 'CPU', color: CPU, values: cpuLine },
                { name: 'Memory', color: MEM, values: memLine },
              ]}
              label={`CPU and memory use over the last ${minutes} minutes`}
            />
          )}
        </div>
        <p className="mt-2 text-xs text-muted">History is kept by the relay in memory for 6 hours and restarts with it.</p>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        {(['cpuUsage', 'memUsage'] as const).map((key) => (
          <Card key={key} title={key === 'cpuUsage' ? 'Top pods by CPU' : 'Top pods by memory'}>
            {usage.isPending ? (
              <Loading />
            ) : !usage.data?.metrics ? (
              <p className="py-4 text-sm text-muted">Needs metrics-server.</p>
            ) : (
              <ul className="mt-2 divide-y divide-line">
                {top(key).map((p) => {
                  const max = top(key)[0][key] || 1
                  return (
                    <li key={`${p.namespace}/${p.name}`}>
                      <button onClick={() => setOpenPod(p)} className="w-full py-2 text-left hover:bg-sunken/60">
                        <span className="flex items-baseline justify-between gap-3">
                          <span className="min-w-0 truncate text-sm font-medium hover:text-blue">{p.name}</span>
                          <span className="shrink-0 text-sm tabular-nums">{key === 'cpuUsage' ? fmtCPU(p.cpuUsage) : fmtMem(p.memUsage)}</span>
                        </span>
                        <span className="block text-xs text-muted">{p.namespace}</span>
                        <span className="mt-1 block h-1 overflow-hidden rounded-full bg-sunken" aria-hidden>
                          <span
                            className="block h-full rounded-full"
                            style={{ width: `${(p[key] / max) * 100}%`, background: key === 'cpuUsage' ? CPU : MEM }}
                          />
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
          </Card>
        ))}

        <Card title="Pods per namespace">
          {pods.isPending ? (
            <Loading />
          ) : (
            <ul className="mt-2 space-y-2">
              {perNs.map((r) => (
                <li key={r.ns} className="grid grid-cols-[minmax(0,9rem)_1fr_auto] items-center gap-3 text-sm">
                  <span className="truncate text-muted" title={r.ns}>
                    {r.ns || '—'}
                  </span>
                  <span className="h-2 overflow-hidden rounded-full bg-sunken" aria-hidden>
                    <span className="block h-full rounded-full" style={{ width: `${(r.total / nsMax) * 100}%`, background: CPU }} />
                  </span>
                  <span className="text-right tabular-nums">
                    {r.total}
                    {r.bad > 0 && <span className="ml-1.5 text-xs text-bad">{r.bad} failing</span>}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title="Latest problems">
          {problems.isPending ? (
            <Loading />
          ) : (problems.data ?? []).length === 0 ? (
            <p className="py-4 text-sm text-muted">Nothing needs your attention right now.</p>
          ) : (
            <ul className="mt-2 divide-y divide-line">
              {(problems.data ?? []).slice(0, 5).map((p) => (
                <li key={p.id}>
                  <button
                    onClick={() => navigate(`/c/${encodeURIComponent(id)}/problems`)}
                    className="flex w-full items-baseline gap-3 py-2 text-left hover:bg-sunken/60"
                  >
                    <span className={`text-sm font-medium ${p.severity === 'critical' ? 'text-bad' : 'text-warn'}`}>{p.reason}</span>
                    <span className="min-w-0 flex-1 truncate text-sm text-muted">
                      {p.namespace ? `${p.namespace}/` : ''}
                      {p.object}
                    </span>
                    <span className="shrink-0 text-xs text-muted tabular-nums">{timeAgo(p.lastSeen)} ago</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      {openPod && <PodPanel cluster={id} namespace={openPod.namespace} name={openPod.name} onClose={() => setOpenPod(null)} />}
    </div>
  )
}