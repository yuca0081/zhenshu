import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 后端地址可用环境变量覆盖（双实例时指 8081）：VITE_API_TARGET=http://127.0.0.1:8081
const apiTarget = process.env.VITE_API_TARGET || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
    },
  },
})
