import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  // The app calls same-origin /api and /media (nginx proxies them in Docker).
  // `npm run dev` had no equivalent, so every request hit the Vite server
  // and failed - point BACKEND_URL at a running backend (default: the
  // docker-compose backend published on localhost:8081).
  const backend = env.BACKEND_URL || 'http://localhost:8081'

  return {
    plugins: [vue()],
    server: {
      proxy: {
        '/api': { target: backend, changeOrigin: true },
        '/media': { target: backend, changeOrigin: true },
      },
    },
  }
})
