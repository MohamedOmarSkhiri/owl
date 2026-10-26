import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Pause, Play, RefreshCw } from 'lucide-react'
import { api, PODS, type ResourceKind } from '../api'
import { ErrorNote, Loading, Panel } from './ui'

const TAILS = [100, 500, 2000]
const LIVE_EVERY = 1_000 // live mode: ask the agent for new lines every 2 s
const LIVE_MAX = 5_000 // keep at most this many live lines in the browser

function Code({ text }: { text: string }) {
  return (
    <pre className="min-h-full overflow-x-auto bg-sunken px-5 py-4 font-mono text-[12.5px] leading-relaxed whitespace-pre">
      {text}
    </pre>
  )
}

/** A pod: container picker, last N log lines, and its JSON. */
export function PodPanel({
  cluster,
  namespace,
  name,
  onClose,
}: {
  cluster: string
  namespace: string
  name: string
  onClose: () => void
}) {
  const [view, setView] = useState<'logs' | 'json'>('logs')
  const [tail, setTail] = useState(100)
  const [picked, setPicked] = useState<string>()

  const pod = useQuery({
    queryKey: ['get', cluster, 'pods', namespace, name],
    queryFn: () => api.get(cluster, PODS, namespace, name),
  })
  const spec = (pod.data as { spec?: { containers?: { name: string }[] } } | undefined)?.spec
  const containers = spec?.containers?.map((c) => c.name) ?? []
  const container = picked ?? containers[0]

  const logs = useQuery({
    queryKey: ['logs', cluster, namespace, name, container, tail],
    queryFn: () => api.logs(cluster, namespace, name, container, tail),
    enabled: view === 'logs' && !!container,
  })

  // Live mode: lines written after the first load are appended below it.
  const [live, setLive] = useState(true)
  const [more, setMore] = useState<string[]>([])
  const cursor = useRef<string | undefined>(undefined) // timestamp of the newest line we have
  const bottom = useRef<HTMLDivElement>(null)

  // A fresh load (other container, other tail, Reload) restarts live mode from its newest line.
  useEffect(() => {
    setMore([])
    cursor.current = logs.data?.last
  }, [logs.data])

  // Every 2 s, ask only for lines newer than the cursor. setTimeout (not setInterval)
  // so a slow answer never overlaps the next request.
  useEffect(() => {
    if (!live || view !== 'logs' || !container || !logs.data) return
    let stopped = false
    let timer = 0
    const tick = async () => {
      try {
        const r = await api.logs(cluster, namespace, name, container, tail, cursor.current)
        if (stopped) return
        if (r.last) cursor.current = r.last
        if (r.lines.length > 0) setMore((m) => [...m, ...r.lines].slice(-LIVE_MAX))
      } catch {
        // pod restarting or agent briefly away: try again on the next tick
      }
      if (!stopped) timer = window.setTimeout(tick, LIVE_EVERY)
    }
    timer = window.setTimeout(tick, LIVE_EVERY)
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [live, view, cluster, namespace, name, container, tail, logs.data])

  // Follow the end of the log while live, like kubectl logs -f. Pause to scroll back and read.
  useEffect(() => {
    if (live) bottom.current?.scrollIntoView({ block: 'end' })
  }, [live, more, logs.data])

  const lines = [...(logs.data?.lines ?? []), ...more]

  return (
    <Panel title={name} subtitle={`Pod in ${namespace}`} onClose={onClose}>
      <div className="sticky top-0 z-10 flex flex-wrap items-center gap-2 border-b border-line bg-surface px-5 py-3">
        <div className="flex rounded-md border border-line p-0.5" role="tablist" aria-label="View">
          {(['logs', 'json'] as const).map((v) => (
            <button
              key={v}
              role="tab"
              aria-selected={view === v}
              onClick={() => setView(v)}
              className={`rounded px-3 py-1 text-sm ${view === v ? 'bg-blue text-white' : 'text-muted hover:text-ink'}`}
            >
              {v === 'logs' ? 'Logs' : 'JSON'}
            </button>
          ))}
        </div>
        {view === 'logs' && (
          <>
            {containers.length > 1 && (
              <label className="flex items-center gap-2 text-sm text-muted">
                Container
                <select
                  value={container}
                  onChange={(e) => setPicked(e.target.value)}
                  className="h-8 rounded-md border border-line bg-surface px-2 text-ink"
                >
                  {containers.map((c) => (
                    <option key={c}>{c}</option>
                  ))}
                </select>
              </label>
            )}
            <label className="flex items-center gap-2 text-sm text-muted">
              Last
              <select
                value={tail}
                onChange={(e) => setTail(Number(e.target.value))}
                className="h-8 rounded-md border border-line bg-surface px-2 text-ink"
              >
                {TAILS.map((t) => (
                  <option key={t} value={t}>
                    {t} lines
                  </option>
                ))}
              </select>
            </label>
            <button
              onClick={() => setLive((v) => !v)}
              aria-pressed={live}
              className="ml-auto flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-blue hover:bg-sunken"
              title={live ? 'Pause to scroll back and read' : 'Follow new lines'}
            >
              {live ? (
                <>
                  <span className="size-2 animate-pulse rounded-full bg-[var(--color-ok)]" aria-hidden />
                  <Pause size={15} />
                  Live
                </>
              ) : (
                <>
                  <Play size={15} />
                  Paused
                </>
              )}
            </button>
            <button
              onClick={() => logs.refetch()}
              className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-blue hover:bg-sunken"
            >
              <RefreshCw size={15} className={logs.isFetching ? 'animate-spin' : ''} />
              Reload
            </button>
          </>
        )}
      </div>
      {view === 'json' ? (
        pod.isPending ? (
          <Loading />
        ) : pod.isError ? (
          <ErrorNote error={pod.error} onRetry={() => pod.refetch()} />
        ) : (
          <Code text={JSON.stringify(pod.data, null, 2)} />
        )
      ) : pod.isError ? (
        <ErrorNote error={pod.error} onRetry={() => pod.refetch()} />
      ) : logs.isPending ? (
        <Loading label="Fetching logs" />
      ) : logs.isError ? (
        <ErrorNote error={logs.error} onRetry={() => logs.refetch()} />
      ) : lines.length === 0 ? (
        <p className="p-5 text-sm text-muted">
          {live ? 'No logs yet. New lines will appear here.' : 'This container has not written any logs.'}
        </p>
      ) : (
        <Code text={lines.join('\n')} />
      )}
      <div ref={bottom} />
    </Panel>
  )
}

/** Any other object: its full JSON (managedFields removed by the agent). */
export function ObjectPanel({
  cluster,
  kind,
  namespace,
  name,
  onClose,
}: {
  cluster: string
  kind: ResourceKind
  namespace?: string
  name: string
  onClose: () => void
}) {
  const q = useQuery({
    queryKey: ['get', cluster, kind.group, kind.resource, namespace, name],
    queryFn: () => api.get(cluster, kind, namespace, name),
  })
  return (
    <Panel title={name} subtitle={namespace ? `${kind.kind} in ${namespace}` : kind.kind} onClose={onClose}>
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorNote error={q.error} onRetry={() => q.refetch()} />
      ) : (
        <Code text={JSON.stringify(q.data, null, 2)} />
      )}
    </Panel>
  )
}
