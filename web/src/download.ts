// Download helpers: build a file in the browser and hand it to the user. Nothing is stored on a server.
import { stringify } from 'yaml'

/** Saves text as a file through a temporary link (the browser's normal download). */
export function saveFile(name: string, text: string, type = 'text/plain') {
  const url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1_000) // free the memory once the download started
}

/** Kubernetes object -> YAML, like kubectl get -o yaml (keys keep their order). */
export function toYaml(obj: unknown): string {
  return stringify(obj, { lineWidth: 0 }) // lineWidth 0: never fold long strings
}

/** 2026-10-26T21:30:05Z -> "20261026-213005", for file names that sort by time. */
export function stamp(d = new Date()): string {
  return d.toISOString().slice(0, 19).replace(/[-:]/g, '').replace('T', '-')
}