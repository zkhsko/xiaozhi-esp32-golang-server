import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  base: '/admin/',
  build: {
    outDir: resolve(__dirname, '../internal/admin/dist'),
    emptyOutDir: true,
  },
  server: {
    port: 3000,
    proxy: {
      '/admin-api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
