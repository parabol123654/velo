import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // emptyOutDir deletes the tracked dist/.gitkeep, which lets `go build`
    // embed web/dist before the UI is built. Put it back after each build.
    {
      name: 'keep-gitkeep',
      closeBundle() {
        writeFileSync(new URL('./dist/.gitkeep', import.meta.url), '');
      },
    },
  ],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
