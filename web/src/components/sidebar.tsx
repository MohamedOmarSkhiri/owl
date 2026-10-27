// Sidebar: clusters with their health at a glance, the sections of the open cluster,
// "watch another cluster", and a footer (theme, project link, version).
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { NavLink } from 'react-router'
import { Bell, BellOff, Check, CircleCheck, CircleX, Copy, ExternalLink, LogOut, Monitor, Moon, Plus, Sun } from 'lucide-react'
import { api, percent, type Cluster, type NotifyStatus } from '../api'
import { getTheme, setTheme, type Theme } from '../theme'
import { ErrorNote, HealthDot, inputClass, Loading, OwlMark, Panel } from './ui'

/** The sections of a cluster, in order. `count` feeds the small number next to the label. */
export const SECTIONS = [
  { key: 'overview', label: 'Overview', count: undefined },
  { key: 'problems', label: 'Problems', count: (c: Cluster) => c.problemsOpen },
  { key: 'pods', label: 'Pods', count: (c: Cluster) => c.podsTotal },
  { key: 'explore', label: 'Explore', count: undefined },
  { key: 'usage', label: 'Usage', count: undefined },
] as const

const REPO = 'https://github.com/MohamedOmarSkhiri/owl'
const CHART = 'oci://ghcr.io/mohamedomarskhiri/charts/owl'

/** A thin CPU or memory bar inside a cluster card. */
function MiniBar({ label, value, color }: { label: string; value: number | null; color: string }) {
  if (value === null) return null
  return (
    <span className="grid grid-cols-[2.25rem_1fr_2.25rem] items-center gap-2 text-[11px] text-muted">
      {label}
      <span className="h-1 overflow-hidden rounded-full bg-sunken" aria-hidden>
        <span className="block h-full rounded-full" style={{ width: `${Math.min(value, 100)}%`, background: color }} />
      </span>
      <span className="text-right tabular-nums">{value}%</span>
    </span>
  )
}

function ThemeSwitch() {
  const [theme, set] = useState<Theme>(getTheme)
  const options = [
    { t: 'auto' as const, icon: <Monitor size={14} />, label: 'Follow system' },
    { t: 'light' as const, icon: <Sun size={14} />, label: 'Light' },
    { t: 'dark' as const, icon: <Moon size={14} />, label: 'Dark' },
  ]
  return (
    <div className="flex rounded-md border border-line p-0.5" role="group" aria-label="Theme">
      {options.map((o) => (
        <button
          key={o.t}
          aria-pressed={theme === o.t}
          title={o.label}
          aria-label={o.label}
          onClick={() => {
            setTheme(o.t)
            set(o.t)
          }}
          className={`rounded p-1 ${theme === o.t ? 'bg-sunken text-ink' : 'text-muted hover:text-ink'}`}
        >
          {o.icon}
        </button>
      ))}
    </div>
  )
}

/** A command with a copy button. Clipboard needs HTTPS or localhost; otherwise select and copy. */
export function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="relative">
      <pre className="rounded-lg bg-sunken p-3 pr-10 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">{text}</pre>
      <button
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(text)
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
          } catch {
            // no clipboard access on plain http: the text stays selectable
          }
        }}
        className="absolute top-2 right-2 rounded p-1 text-muted hover:bg-surface hover:text-ink"
        aria-label="Copy command"
      >
        {copied ? <Check size={15} /> : <Copy size={15} />}
      </button>
    </div>
  )
}

function AddClusterPanel({ index, onClose }: { index: number; onClose: () => void }) {
  const [name, setName] = useState('prod')
  const n = name.trim() || 'prod'
  return (
    <Panel title="Watch another cluster" subtitle="Owl stays read-only on every cluster" onClose={onClose} wide={false}>
      <div className="space-y-5 p-5 text-sm">
        <label className="block">
          <span className="font-medium">Name shown in Owl</span>
          <input value={name} onChange={(e) => setName(e.target.value)} className={`${inputClass} mt-1.5 w-full`} />
        </label>
        <div>
          <p className="font-medium">1. Export a kubeconfig for that cluster</p>
          <p className="mt-1 text-muted">
            Use a read-only account, never an admin one. The file holds credentials: keep it out of Git.
          </p>
          <div className="mt-2">
            <Command text={`kubectl config view --minify --flatten --context ${n} > ${n}.yaml`} />
          </div>
        </div>
        <div>
          <p className="font-medium">2. Add it to Owl</p>
          <p className="mt-1 text-muted">A new agent starts in this cluster and watches the other one through the kubeconfig.</p>
          <div className="mt-2">
            <Command
              text={`helm upgrade owl ${CHART} -n owl --reset-then-reuse-values \\\n  --set 'clusters[${index}].name=${n}' \\\n  --set-file 'clusters[${index}].kubeconfig=${n}.yaml'`}
            />
          </div>
        </div>
        <p className="text-muted">
          It appears in the sidebar within 15 seconds. Release name or namespace different from <code>owl</code>? Change them
          in the command.
        </p>
      </div>
    </Panel>
  )
}

const CHANNEL_NAMES = { telegram: 'Telegram', slack: 'Slack', discord: 'Discord', webhook: 'Webhook' }

/** "10m0s" (Go) -> "10 minutes" */
function duration(go = '10m0s') {
  const h = Number(/(\d+)h/.exec(go)?.[1] ?? 0)
  const m = Number(/(\d+)m(?!s)/.exec(go)?.[1] ?? 0)
  const total = h * 60 + m
  if (total === 0) return go
  return total % 60 === 0 ? `${total / 60} hour${total === 60 ? '' : 's'}` : `${total} minute${total === 1 ? '' : 's'}`
}

/** What is sent, where, and a test button. Set up happens in the Helm chart, so secrets stay in a Secret. */
function NotificationsPanel({ status, onClose }: { status: NotifyStatus | undefined; onClose: () => void }) {
  const test = useMutation({ mutationFn: api.testNotifications })
  return (
    <Panel title="Notifications" subtitle="A message when a problem opens or a cluster goes down" onClose={onClose} wide={false}>
      <div className="space-y-6 p-5 text-sm">
        {!status ? (
          <Loading />
        ) : status.enabled ? (
          <>
            <section>
              <h3 className="font-medium">Sending to</h3>
              <ul className="mt-2 divide-y divide-line rounded-lg border border-line">
                {status.channels.map((c) => (
                  <li key={c.name} className="flex items-baseline justify-between gap-3 px-3 py-2">
                    <span className="font-medium">{CHANNEL_NAMES[c.name] ?? c.name}</span>
                    <span className="truncate text-muted">{c.target}</span>
                  </li>
                ))}
              </ul>
            </section>
            <section className="space-y-1.5 text-muted">
              <p>
                {status.minSeverity === 'warning' ? 'Critical and warning problems' : 'Critical problems'}
                {status.resolved ? ', when they are resolved, ' : ' '}
                and clusters going down{status.resolved ? ' or coming back' : ''}.
              </p>
              <p>At most one message per problem every {duration(status.cooldown)}.</p>
              <p>
                {status.publicUrl ? (
                  <>Messages link to {status.publicUrl}.</>
                ) : (
                  <>Set notifications.publicUrl in the chart so messages link to the problem in Owl.</>
                )}
              </p>
            </section>
            <section>
              <button
                onClick={() => test.mutate()}
                disabled={test.isPending}
                className="h-10 w-full rounded-md bg-blue font-medium text-white hover:bg-blue-strong disabled:opacity-60"
              >
                {test.isPending ? 'Sending…' : 'Send a test message'}
              </button>
              {test.isError && <ErrorNote error={test.error} />}
              {test.data && (
                <ul className="mt-3 space-y-1.5" aria-live="polite">
                  {test.data.map((r) => (
                    <li key={r.name} className="flex items-start gap-2">
                      {r.ok ? (
                        <CircleCheck size={17} className="mt-px shrink-0 text-ok" aria-label="Sent" />
                      ) : (
                        <CircleX size={17} className="mt-px shrink-0 text-bad" aria-label="Failed" />
                      )}
                      <span>
                        <span className="font-medium">{CHANNEL_NAMES[r.name] ?? r.name}</span>
                        <span className="text-muted">{r.ok ? ': sent, check your app' : `: ${r.error}`}</span>
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </>
        ) : (
          <>
            <p className="text-muted">
              No channel is set up yet. Notifications are set in the Helm chart, so tokens stay in a Kubernetes Secret.
              Telegram takes two minutes:
            </p>
            <div>
              <p className="font-medium">1. Create a bot</p>
              <p className="mt-1 text-muted">
                In Telegram, talk to @BotFather, send /newbot and copy the token. Then send any message to your new bot.
              </p>
            </div>
            <div>
              <p className="font-medium">2. Find your chat id</p>
              <div className="mt-2">
                <Command text={`curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | grep -o '"chat":{"id":-\\?[0-9]*'`} />
              </div>
            </div>
            <div>
              <p className="font-medium">3. Turn it on</p>
              <div className="mt-2">
                <Command
                  text={`helm upgrade owl ${CHART} -n owl --reset-then-reuse-values \\\n  --set notifications.telegram.token=<TOKEN> \\\n  --set notifications.telegram.chatId=<CHAT_ID> \\\n  --set notifications.publicUrl=${window.location.origin}`}
                />
              </div>
            </div>
            <p className="text-muted">
              Slack, Discord or any webhook:{' '}
              <a href={`${REPO}/tree/main/charts/owl#notifications`} target="_blank" rel="noreferrer" className="text-blue hover:underline">
                see the chart README
              </a>
              .
            </p>
          </>
        )}
      </div>
    </Panel>
  )
}

export function Sidebar({
  clusters,
  activeId,
  activeTab,
  onSignOut,
}: {
  clusters: Cluster[]
  activeId?: string
  activeTab: string
  onSignOut: () => void
}) {
  const [adding, setAdding] = useState(false)
  const [bell, setBell] = useState(false)
  // Older relays have no /v1/notifications: the bell then opens the set-up help.
  const notify = useQuery({ queryKey: ['notifications'], queryFn: api.notifications, staleTime: 60_000, retry: false })
  // Older relays have no /v1/info: the version is simply not shown.
  const info = useQuery({ queryKey: ['info'], queryFn: api.info, staleTime: Infinity, retry: false })

  return (
    <aside className="flex shrink-0 flex-col border-b border-line bg-surface md:w-72 md:border-r md:border-b-0">
      <div className="flex items-center gap-2.5 px-5 py-4">
        <OwlMark size={34} />
        <span className="text-xl font-semibold tracking-tight">Owl</span>
        <button
          onClick={onSignOut}
          className="ml-auto rounded-md p-1.5 text-muted hover:bg-sunken hover:text-ink"
          aria-label="Sign out"
          title="Sign out"
        >
          <LogOut size={18} />
        </button>
      </div>

      <p className="hidden px-5 pb-1 text-xs font-medium tracking-wide text-muted uppercase md:block">Clusters</p>
      <nav aria-label="Clusters" className="flex gap-1 overflow-x-auto px-3 pb-3 md:flex-col md:overflow-visible">
        {clusters.map((c) => {
          const active = c.id === activeId
          const base = `/c/${encodeURIComponent(c.id)}`
          return (
            <div key={c.id} className={`min-w-44 rounded-lg md:min-w-0 ${active ? 'bg-sunken' : ''}`}>
              <NavLink
                to={`${base}/overview`}
                className={`flex items-center gap-3 rounded-lg px-3 py-2.5 ${active ? 'text-ink' : 'text-muted hover:bg-sunken/60 hover:text-ink'}`}
              >
                <HealthDot health={c.health} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium text-ink">{c.name}</span>
                  <span className="block truncate text-xs">{c.k8sVersion}</span>
                </span>
                {(c.problemsOpen ?? 0) > 0 && (
                  <span
                    className="rounded-full bg-bad/15 px-2 py-0.5 text-xs font-medium text-bad tabular-nums"
                    title={`${c.problemsOpen} open problems`}
                  >
                    {c.problemsOpen}
                  </span>
                )}
              </NavLink>
              {c.usage && (
                <span className="hidden space-y-1 px-3 pb-2.5 md:block">
                  <MiniBar label="CPU" value={percent(c.usage.cpuUsage, c.usage.cpuAllocatable)} color="var(--color-series-1)" />
                  <MiniBar label="Mem" value={percent(c.usage.memUsage, c.usage.memAllocatable)} color="var(--color-series-2)" />
                </span>
              )}
              {active && (
                <ul className="hidden border-t border-line/70 py-1.5 md:block" aria-label={`${c.name} sections`}>
                  {SECTIONS.map((s) => {
                    const count = s.count?.(c)
                    const on = activeTab === s.key
                    return (
                      <li key={s.key}>
                        <NavLink
                          to={`${base}/${s.key}`}
                          aria-current={on ? 'page' : undefined}
                          className={`mx-1.5 flex items-center rounded-md px-3 py-1.5 text-sm ${
                            on ? 'bg-surface font-medium text-ink shadow-sm' : 'text-muted hover:text-ink'
                          }`}
                        >
                          {s.label}
                          {count !== undefined && (
                            <span className={`ml-auto text-xs tabular-nums ${s.key === 'problems' && count > 0 ? 'font-medium text-bad' : ''}`}>
                              {count}
                            </span>
                          )}
                        </NavLink>
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>
          )
        })}
        <button
          onClick={() => setAdding(true)}
          className="hidden items-center gap-2 rounded-lg px-3 py-2.5 text-sm text-blue hover:bg-sunken/60 md:flex"
        >
          <Plus size={16} />
          Watch another cluster
        </button>
      </nav>

      <footer className="mt-auto hidden items-center gap-3 border-t border-line px-5 py-3 text-xs text-muted md:flex">
        <ThemeSwitch />
        <button
          onClick={() => setBell(true)}
          className="rounded p-1 hover:bg-sunken hover:text-ink"
          aria-label="Notifications"
          title={notify.data?.enabled ? 'Notifications are on' : 'Notifications are off'}
        >
          {notify.data?.enabled ? <Bell size={15} className="text-ok" /> : <BellOff size={15} />}
        </button>
        <a href={REPO} target="_blank" rel="noreferrer" className="flex items-center gap-1 hover:text-ink">
          GitHub
          <ExternalLink size={12} />
        </a>
        {info.data && <span className="ml-auto tabular-nums">{info.data.version}</span>}
      </footer>

      {adding && <AddClusterPanel index={clusters.length} onClose={() => setAdding(false)} />}
      {bell && (
        <NotificationsPanel
          status={notify.data ?? (notify.isError ? { enabled: false, channels: [] } : undefined)}
          onClose={() => setBell(false)}
        />
      )}
    </aside>
  )
}
