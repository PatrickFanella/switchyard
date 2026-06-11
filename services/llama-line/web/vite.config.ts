import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const GO_TARGET = process.env.LLAMA_LINE_TARGET || 'http://127.0.0.1:11434'

export default defineConfig({
  base: '/ui/',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    port: 5173,
    proxy: {
      '/admin':       { target: GO_TARGET, changeOrigin: true },
      '/ui/api':      { target: GO_TARGET, changeOrigin: true },
      '/broker':      { target: GO_TARGET, changeOrigin: true },
      '/metrics':     { target: GO_TARGET, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
