import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  root: 'web',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../dist-web',
    emptyOutDir: true,
  },
  test: {
    include: ['../src/**/*.{test,spec}.?(c|m)[jt]s?(x)'],
  },
});
