import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { BellOff } from 'lucide-react'
import { api, type Mute, type MuteRequest, type Problem } from '../api'
import { inputClass } from './ui'

const btn = 'rounded-md px-2.5 py-1 text-sm text-blue hover:bg-sunken disabled:opacity-50'

const durations = [
  { label: '1 hour', for: '1h' },
  { label: '1 day', for: '24h' },
  { label: '1 week', for: '168h' },
  { label: 'Until resolved', for: '' },
] as const

type Scope = 'problem' | 'reason' | 'namespace'

/** What a mute covers, in words: "pod/web CrashLoopBackOff", "every ReplicasNotReady in demo"… */
export function muteScope(m: Mute): string {
  if (m.object && m.reason) return `${m.reason} on ${m.object}`
  const where = m.namespace ? ` in ${m.namespace}` : ' in this cluster'
  if (m.reason) return `every ${m.reason}${where}`
  if (m.object) return `everything about ${m.object}`
  return `everything${where}`
}

function until(m: Mute): string {
  if (m.untilResolved) return 'until resolved'
  if (!m.until) return ''
  const d = new Date(m.until)
  const sameDay = d.toDateString() === new Date().toDateString()
  return `until ${sameDay ? '' : d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' }) + ' '}${d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}`
}

function useMuteActions(cluster: string) {
  const qc = useQueryClient()
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['problems', cluster] })
    qc.invalidateQueries({ queryKey: ['mutes', cluster] })
  }
  const add = useMutation({ mutationFn: (m: MuteRequest) => api.mute(cluster, m), onSuccess: refresh })
  const remove = useMutation({ mutationFn: (id: string) => api.unmute(cluster, id), onSuccess: refresh })
  return { add, remove }
}

/** Under a muted problem: what silences it, its note, and Unmute. */
export function MutedLine({ cluster, mute }: { cluster: string; mute: Mute }) {
  const { remove } = useMuteActions(cluster)
  const own = !!(mute.object && mute.reason)
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted">
      <BellOff size={14} aria-hidden />
      <span>
        {own && mute.untilResolved && mute.note ? 'Acknowledged' : 'Muted'} {until(mute)}
        {!own && ` (${muteScope(mute)})`}
        {mute.note && <span className="text-ink"> · {mute.note}</span>}
      </span>
      <button className={btn} disabled={remove.isPending} onClick={() => remove.mutate(mute.id)}>
        {own ? 'Unmute' : 'Unmute all'}
      </button>
      {remove.isError && <span className="text-bad">{(remove.error as Error).message}</span>}
    </div>
  )
}

/** The Mute button and, once opened, the small form: what, for how long, a note. */
export function MuteControl({ cluster, problem }: { cluster: string; problem: Problem }) {
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState<Scope>('problem')
  const [dur, setDur] = useState<string>('24h')
  const [note, setNote] = useState('')
  const { add } = useMuteActions(cluster)

  if (!open)
    return (
      <button className={`${btn} flex items-center gap-1.5`} onClick={() => setOpen(true)}>
        <BellOff size={14} aria-hidden /> Mute
      </button>
    )

  const ns = problem.namespace
  const scopes: { value: Scope; label: string }[] = [
    { value: 'problem', label: 'This problem' },
    { value: 'reason', label: `Every ${problem.reason}${ns ? ` in ${ns}` : ''}` },
    ...(ns ? [{ value: 'namespace' as Scope, label: `Everything in ${ns}` }] : []),
  ]
  const untilResolved = dur === ''
  const canResolve = scope === 'problem'

  const save = () => {
    const m: MuteRequest = { namespace: ns || undefined, note: note.trim() || undefined }
    if (scope === 'problem') Object.assign(m, { object: problem.object, reason: problem.reason })
    if (scope === 'reason') m.reason = problem.reason
    if (untilResolved && canResolve) m.untilResolved = true
    else m.for = dur || '24h'
    add.mutate(m, { onSuccess: () => setOpen(false) })
  }

  return (
    <form
      className="mt-2 flex flex-col gap-3 rounded-lg border border-line bg-sunken/40 p-3"
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <fieldset className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
        <legend className="mb-1 text-xs font-medium text-muted uppercase">Mute</legend>
        {scopes.map((s) => (
          <label key={s.value} className="flex items-center gap-1.5">
            <input
              type="radio"
              name={`scope-${problem.id}`}
              checked={scope === s.value}
              onChange={() => {
                setScope(s.value)
                if (s.value !== 'problem' && dur === '') setDur('24h')
              }}
            />
            {s.label}
          </label>
        ))}
      </fieldset>
      <fieldset className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
        <legend className="mb-1 text-xs font-medium text-muted uppercase">For</legend>
        {durations
          .filter((d) => d.for !== '' || canResolve)
          .map((d) => (
            <label key={d.label} className="flex items-center gap-1.5">
              <input type="radio" name={`for-${problem.id}`} checked={dur === d.for} onChange={() => setDur(d.for)} />
              {d.label}
            </label>
          ))}
      </fieldset>
      <input
        className={inputClass}
        value={note}
        maxLength={500}
        onChange={(e) => setNote(e.target.value)}
        placeholder="Note, optional: known, fixing Monday"
        aria-label="Note"
      />
      <div className="flex flex-wrap items-center gap-2">
        <button type="submit" disabled={add.isPending} className="rounded-md bg-blue px-3 py-1.5 text-sm text-white disabled:opacity-50">
          {untilResolved && canResolve && note.trim() ? 'Acknowledge' : 'Mute'}
        </button>
        <button type="button" className={btn} onClick={() => setOpen(false)}>
          Cancel
        </button>
        <span className="text-xs text-muted">Muted problems stay listed but send no notifications.</span>
        {add.isError && <span className="text-sm text-bad">{(add.error as Error).message}</span>}
      </div>
    </form>
  )
}

/** Above the list: mutes wider than one problem (a namespace, a reason), so none is forgotten. */
export function ActiveMutes({ cluster, mutes }: { cluster: string; mutes: Mute[] }) {
  const { remove } = useMuteActions(cluster)
  const wide = mutes.filter((m) => !(m.object && m.reason))
  if (wide.length === 0) return null
  return (
    <ul className="flex flex-col gap-1 border-b border-line bg-sunken/40 px-5 py-2.5 text-sm">
      {wide.map((m) => (
        <li key={m.id} className="flex flex-wrap items-center gap-x-2 text-muted">
          <BellOff size={14} aria-hidden />
          <span>
            Muted: <span className="text-ink">{muteScope(m)}</span> {until(m)}
            {m.note && <span> · {m.note}</span>}
          </span>
          <button className={btn} disabled={remove.isPending} onClick={() => remove.mutate(m.id)}>
            Unmute
          </button>
        </li>
      ))}
    </ul>
  )
}
