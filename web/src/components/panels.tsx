import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { api, PODS, type ResourceKind } from '../api'
import { ErrorNote, Loading, Panel } from './ui'

const TAILS = [100, 500, 2000]

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
              onClick={() => logs.refetch()}
              className="ml-auto flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-blue hover:bg-sunken"
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
      ) : logs.data.lines.length === 0 ? (
        <p className="p-5 text-sm text-muted">This container has not written any logs.</p>
      ) : (
        <Code text={logs.data.lines.join('\n')} />
      )}
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
