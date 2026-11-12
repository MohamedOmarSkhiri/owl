// Certificates: every certificate the agent watches (apps' HTTPS hosts, cert-manager, the API
// server, kubelets, the kubeconfig Owl signs in with, Owl's own), first to expire on top.
// The agent checks them every 10 minutes; problems are raised from 14 days before expiry.
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, timeAgo, type CertItem } from '../api'
import { objectTarget, OpenPanel, SearchBox, type Open } from './tabs'
import { ErrorNote, Loading, Pill } from './ui'

const kindLabel: Record<CertItem['kind'], string> = {
  'cert-manager': 'cert-manager',
  host: 'HTTPS host',
  apiserver: 'API server',
  kubelet: 'Kubelet',
  kubeconfig: 'Kubeconfig',
  service: 'Owl',
}

const statusPill: Record<CertItem['status'], { tone: 'ok' | 'warn' | 'bad' | 'plain'; label: string }> = {
  ok: { tone: 'ok', label: 'OK' },
  warning: { tone: 'warn', label: 'Expires soon' },
  critical: { tone: 'bad', label: 'Expiring' },
  notReady: { tone: 'bad', label: 'Not ready' },
  unreachable: { tone: 'plain', label: 'Not reachable' },
  otherName: { tone: 'plain', label: 'Not checked' },
}

const DAY = 86_400_000

function left(notAfter: string): string {
  const ms = new Date(notAfter).getTime() - Date.now()
  const days = Math.floor(Math.abs(ms) / DAY)
  const span = days >= 2 ? `${days} days` : `${Math.max(1, Math.floor(Math.abs(ms) / 3_600_000))} hours`
  return ms < 0 ? `expired ${span} ago` : `in ${span}`
}

function where(c: CertItem): string {
  if (c.object.startsWith('cluster/')) return 'cluster'
  return c.namespace ? `${c.namespace}/${c.object}` : c.object
}

export function CertificatesTab({ cluster }: { cluster: string }) {
  const [text, setText] = useState('')
  const [open, setOpen] = useState<Open>(null)
  // The agent checks every 10 minutes: reading again every minute is plenty.
  const q = useQuery({ queryKey: ['certificates', cluster], queryFn: () => api.certificates(cluster), refetchInterval: 60_000 })

  const items = useMemo(() => {
    const t = text.trim().toLowerCase()
    return (q.data?.items ?? []).filter((c) => !t || `${c.what} ${where(c)} ${kindLabel[c.kind]}`.toLowerCase().includes(t))
  }, [q.data, text])

  if (q.isPending) return <Loading />
  if (q.isError) return <ErrorNote error={q.error} onRetry={() => q.refetch()} />
  const r = q.data
  if (!r.checked) return <Loading label="The agent is checking the certificates (it takes a few seconds after it starts)" />

  const attention = r.items.filter((c) => c.status === 'warning' || c.status === 'critical' || c.status === 'notReady').length

  return (
    <>
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-5 py-3">
        <SearchBox value={text} onChange={setText} label="Filter certificates" />
        <p className="text-sm text-muted">
          {r.items.length} certificates
          {attention > 0 && <span className="font-medium text-bad">, {attention} need attention</span>} · checked {timeAgo(r.checked)} ago,
          every {r.every.replace('m0s', ' min')} · problems open 14 days before expiry (30 for the kubeconfig)
        </p>
      </div>
      {r.items.length === 0 ? (
        <p className="px-6 py-12 text-center text-muted">No certificate found: this cluster has no HTTPS host, and the agent signs in with a token.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-line text-left text-muted">
                <th className="px-4 py-2.5 font-medium">Certificate</th>
                <th className="px-4 py-2.5 font-medium">Expires</th>
                <th className="px-4 py-2.5 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {items.map((c) => {
                const t = objectTarget(c.namespace, c.object)
                const pill = statusPill[c.status]
                return (
                  <tr
                    key={`${c.namespace}|${c.object}|${c.what}`}
                    className={`border-b border-line/70 ${t ? 'cursor-pointer hover:bg-sunken/70' : ''}`}
                    onClick={() => t && setOpen(t)}
                  >
                    <td className="max-w-[32rem] px-4 py-3">
                      {t ? (
                        <button className="block max-w-full truncate text-left font-medium hover:text-blue">{c.what}</button>
                      ) : (
                        <span className="block truncate font-medium">{c.what}</span>
                      )}
                      <span className="block truncate text-xs text-muted">
                        {kindLabel[c.kind]} · {where(c)}
                      </span>
                    </td>
                    <td className="px-4 py-3 whitespace-nowrap tabular-nums">
                      {c.notAfter ? (
                        <>
                          {new Date(c.notAfter).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })}
                          <span className="block text-xs text-muted">{left(c.notAfter)}</span>
                        </>
                      ) : (
                        <span className="text-muted">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <Pill tone={pill.tone}>{pill.label}</Pill>
                      {c.detail && <span className="mt-1 block max-w-80 text-xs text-muted">{c.detail}</span>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <OpenPanel cluster={cluster} open={open} onClose={() => setOpen(null)} />
    </>
  )
}
