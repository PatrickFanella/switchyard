import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/ocq': 'http://127.0.0.1:8088',
      '/v1': 'http://127.0.0.1:8088',
      '/metrics': 'http://127.0.0.1:8088',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
  },
})
