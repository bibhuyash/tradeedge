import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    port: 3000,
    proxy: {
      '/control': { target: 'http://127.0.0.1:8081', changeOrigin: true, rewrite: (path) => path.replace(/^\/control/, '') },
      '/shadow': { target: 'http://127.0.0.1:8080', changeOrigin: true, rewrite: (path) => path.replace(/^\/shadow/, '') },
    },
  },
})
