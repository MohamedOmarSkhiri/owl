// Sign-in page: "the night watch". The owl sits on the heartbeat line, blinks, and its
// eyes follow the pointer; when you type in the token field, it looks at the field.
import { useEffect, useRef, useState } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { api, ApiError, setToken } from '../api'
import { Command } from './sidebar'
import { inputClass, OwlMark } from './ui'

const TOKEN_COMMAND = "kubectl -n owl get secret owl-tokens -o jsonpath='{.data.app-token}' | base64 -d; echo"

/** Direction from the owl to a point on screen, as x and y from -1 to 1. */
function lookAt(owl: HTMLElement | null, px: number, py: number) {
  if (!owl) return { x: 0, y: 0 }
  const r = owl.getBoundingClientRect()
  const dx = px - (r.left + r.width / 2)
  const dy = py - (r.top + r.height / 2)
  const len = Math.hypot(dx, dy) || 1
  const reach = Math.min(1, len / 240) // far away = eyes to the side; close = barely moving
  return { x: (dx / len) * reach, y: (dy / len) * reach }
}

export function SignIn({ onSignedIn }: { onSignedIn: () => void }) {
  const [token, setValue] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [show, setShow] = useState(false)
  const [look, setLook] = useState({ x: 0, y: 0 })
  const owl = useRef<HTMLDivElement>(null)
  const field = useRef<HTMLInputElement>(null)
  const typing = useRef(false) // while the field has focus, the owl keeps looking at it

  // The eyes follow the pointer anywhere on the page.
  useEffect(() => {
    const move = (e: PointerEvent) => !typing.current && setLook(lookAt(owl.current, e.clientX, e.clientY))
    const away = () => !typing.current && setLook({ x: 0, y: 0 })
    window.addEventListener('pointermove', move)
    document.documentElement.addEventListener('pointerleave', away)
    return () => {
      window.removeEventListener('pointermove', move)
      document.documentElement.removeEventListener('pointerleave', away)
    }
  }, [])

  const watchField = () => {
    typing.current = true
    const r = field.current?.getBoundingClientRect()
    if (r) setLook(lookAt(owl.current, r.left + r.width / 2, r.top + r.height / 2))
  }

  const submit = async (e: { preventDefault(): void }) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await api.check(token.trim())
      setToken(token.trim())
      onSignedIn()
    } catch (err) {
      setError(err instanceof ApiError && err.status === 0 ? 'Cannot reach the Owl relay.' : (err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="grid min-h-full md:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
      {/* The night panel stays navy in both themes: it is the owl's night. */}
      <section className="flex flex-col items-center justify-center gap-6 overflow-hidden bg-[#0b1228] px-6 py-10 text-[#e6ecfb] md:py-0">
        <div className="relative w-full max-w-xl">
          <div ref={owl} className="relative z-10 mx-auto w-fit">
            <span className="md:hidden">
              <OwlMark size={104} blink look={look} />
            </span>
            <span className="hidden md:inline">
              <OwlMark size={180} blink look={look} />
            </span>
          </div>
          {/* The heartbeat the owl is perched on: flat under the owl, beating on each side. */}
          <svg viewBox="0 0 600 60" preserveAspectRatio="none" className="pulse-line -mt-8 block h-12 w-full md:-mt-10" aria-hidden="true">
            <path
              d="M0 30 H70 L88 30 L102 6 L122 52 L136 18 L148 30 H452 L464 30 L478 6 L498 52 L512 18 L530 30 H600"
              fill="none"
              stroke="#34d399"
              strokeWidth={3}
              strokeLinecap="round"
              strokeLinejoin="round"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
        </div>
        <p className="max-w-xs text-center text-lg leading-snug text-[#9aa8c9]">
          Watches your Kubernetes clusters.
          <br />
          Changes nothing.
        </p>
      </section>

      <section className="flex items-center justify-center bg-surface px-6 py-12 md:px-12">
        <form onSubmit={submit} className="w-full max-w-sm">
          <h1 className="text-3xl font-semibold tracking-tight">Sign in to Owl</h1>
          <p className="mt-2 text-muted">Paste the app token of your Owl relay.</p>

          <label className="mt-8 block text-sm font-medium" htmlFor="token">
            App token
          </label>
          <div className="relative mt-1.5">
            <input
              id="token"
              ref={field}
              type={show ? 'text' : 'password'}
              autoComplete="current-password"
              spellCheck={false}
              required
              value={token}
              onChange={(e) => setValue(e.target.value)}
              onFocus={watchField}
              onBlur={() => (typing.current = false)}
              className={`${inputClass} h-11 w-full pr-11 font-mono`}
            />
            <button
              type="button"
              onClick={() => setShow((v) => !v)}
              className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1.5 text-muted hover:text-ink"
              aria-label={show ? 'Hide token' : 'Show token'}
              aria-pressed={show}
            >
              {show ? <EyeOff size={17} /> : <Eye size={17} />}
            </button>
          </div>

          {error && (
            <p className="mt-3 text-sm text-bad" role="alert">
              {error}
            </p>
          )}

          <button
            type="submit"
            disabled={busy || !token.trim()}
            className="mt-6 h-11 w-full rounded-md bg-blue font-medium text-white hover:bg-blue-strong disabled:opacity-60"
          >
            {busy ? 'Signing in…' : 'Sign in'}
          </button>

          <details className="group mt-8 border-t border-line pt-4 text-sm">
            <summary className="cursor-pointer list-none font-medium text-blue marker:hidden">
              <span className="group-open:hidden">Where is my token?</span>
              <span className="hidden group-open:inline">Hide help</span>
            </summary>
            <p className="mt-3 text-muted">Installed with Helm: run this, then paste what it prints.</p>
            <div className="mt-2">
              <Command text={TOKEN_COMMAND} />
            </div>
            <p className="mt-3 text-muted">
              Running the relay yourself: it is the value of <code className="font-mono text-ink">RELAY_APP_TOKEN</code>.
            </p>
          </details>
        </form>
      </section>
    </main>
  )
}