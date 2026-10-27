// Theme: follow the system (auto), or force light or dark. Remembered in this browser only.
export type Theme = 'auto' | 'light' | 'dark'

const KEY = 'owl.theme'

export function getTheme(): Theme {
  try {
    const t = localStorage.getItem(KEY)
    return t === 'light' || t === 'dark' ? t : 'auto'
  } catch {
    return 'auto' // storage blocked: just follow the system
  }
}

/** Sets <html data-theme="…">; index.css switches the colour tokens on it. */
export function setTheme(t: Theme) {
  if (t === 'auto') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = t
  try {
    if (t === 'auto') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, t)
  } catch {
    // not remembered, still applied for this visit
  }
}