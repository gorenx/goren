import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        entryFileNames: 'app.js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames(assetInfo) {
          return assetInfo.names.some(name => name.endsWith('.css'))
            ? 'app.css'
            : 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
})
