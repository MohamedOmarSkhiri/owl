import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// In development the relay runs on :2300; Vite forwards API calls to it,
// so the browser sees one origin (no CORS), exactly like in production.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/v1': 'http://localhost:2300',
      '/healthz': 'http://localhost:2300',
    },
  },
})
