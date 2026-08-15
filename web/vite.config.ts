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
        entryFileNames: 'assets/app-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames(assetInfo) {
          return assetInfo.names.some(name => name.endsWith('.css'))
            ? 'assets/app-[hash][extname]'
            : 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
})
