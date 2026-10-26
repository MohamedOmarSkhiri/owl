import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download, Pause, Play, RefreshCw } from 'lucide-react'
import { api, PODS, type ResourceKind } from '../api'
import { saveFile, stamp, toYaml } from '../download'
import { ErrorNote, Loading, Panel } from './ui'

const TAILS = [100, 500, 2000]
const LIVE_EVERY = 1_000 // live mode: ask the agent for new lines every second
const LIVE_MAX = 5_000 // keep at most this many live lines in the browser

function Code({ text }: { text: string }) {
  return (
    <pre className="min-h-full overflow-x-auto bg-sunken px-5 py-4 font-mono text-[12.5px] leading-relaxed whitespace-pre">
      {text}
    </pre>
  )
}

const btn = 'flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-blue hover:bg-sunken disabled:opacity-50'

/** An object's manifest as YAML (default, like kubectl) or JSON, with a download button. */
function Manifest({ data, file }: { data: unknown; file: string }) {
  const [format, setFormat] = useState<'yaml' | 'json'>('yaml')
  const text = format === 'yaml' ? toYaml(data) : JSON.stringify(data, null, 2)
  return (
    <>
      <div className="flex items-center gap-2 border-b border-line px-5 py-2">
        <div className="flex rounded-md border border-line p-0.5" role="group" aria-label="Format">
          {(['yaml', 'json'] as const).map((f) => (
            <button
              key={f}
              aria-pressed={format === f}
              onClick={() => setFormat(f)}
              className={`rounded px-2.5 py-0.5 text-xs uppercase ${format === f ? 'bg-sunken text-ink' : 'text-muted hover:text-ink'}`}
            >
              {f}
            </button>
          ))}
        </div>
        <button
          onClick={() => saveFile(`${file}.${format}`, text, format === 'yaml' ? 'application/yaml' : 'application/json')}
          className={`ml-auto ${btn}`}
        >
          <Download size={15} />
          Download
        </button>
      </div>
      <Code text={text} />
    </>
  )
}

/** A pod: container picker, last N log lines (live), and its manifest. */
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

  // Every second, ask only for lines newer than the cursor. setTimeout (not setInterval)
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
    if (live && view === 'logs') bottom.current?.scrollIntoView({ block: 'end' })
  }, [live, view, more, logs.data])

  // The manifest starts at its top, not where the logs were scrolled to.
  const top = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (view === 'json') top.current?.scrollIntoView({ block: 'start' })
  }, [view])

  const lines = [...(logs.data?.lines ?? []), ...more]

  // Download the whole log of this container (not only the lines on screen), as kubectl logs would.
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string>()
  const downloadLogs = async () => {
    if (!container) return
    setSaving(true)
    setSaveError(undefined)
    try {
      const r = await api.logs(cluster, namespace, name, container, 'all')
      saveFile(`${name}_${container}_${stamp()}.log`, r.lines.join('\n') + '\n')
    } catch (err) {
      setSaveError((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Panel title={name} subtitle={`Pod in ${namespace}`} onClose={onClose}>
      <div ref={top} />
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
              {v === 'logs' ? 'Logs' : 'Manifest'}
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
            <button onClick={downloadLogs} disabled={saving || !container} className={btn} title="Download the whole log">
              <Download size={15} className={saving ? 'animate-pulse' : ''} />
              Download
            </button>
          </>
        )}
      </div>
      {saveError && (
        <p className="border-b border-line px-5 py-2 text-sm text-bad" role="alert">
          Download failed: {saveError}
        </p>
      )}
      {view === 'json' ? (
        pod.isPending ? (
          <Loading />
        ) : pod.isError ? (
          <ErrorNote error={pod.error} onRetry={() => pod.refetch()} />
        ) : (
          <Manifest data={pod.data} file={`${namespace}_pod_${name}`} />
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

/** Any other object: its manifest as YAML or JSON (managedFields removed by the agent). */
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
        <Manifest data={q.data} file={`${namespace ? `${namespace}_` : ''}${kind.kind.toLowerCase()}_${name}`} />
      )}
    </Panel>
  )
}