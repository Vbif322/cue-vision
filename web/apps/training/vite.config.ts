import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv } from 'vite'

// В разработке фронт ходит в cue-vision через прокси: у сервера нет CORS, а токен
// API (CUEVISION_API_TOKEN) подставляет прокси и в браузер он не попадает.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target = env.CUEVISION_API_URL || 'http://localhost:8080'
  const token = env.CUEVISION_API_TOKEN

  return {
    plugins: [vue()],
    resolve: {
      alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
    },
    server: {
      port: 5173,
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
          headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        },
      },
    },
  }
})
