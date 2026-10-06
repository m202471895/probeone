import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

/**
 * Vite 配置
 *
 * 生产产物会被 Go 的 embed.FS 内嵌进二进制，因此：
 *  - 必须输出相对路径（base: './'），否则内嵌后资源引用会指向错误位置
 *  - 资源体积阈值设成实报，让超标的产物立刻暴露而不是悄悄变大
 */
export default defineConfig({
  plugins: [vue()],

  // 内嵌进 Go 二进制后，页面部署在任意子路径下都能工作
  base: './',

  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },

  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2020',
    // 超过 250KB 就该主动优化了（PRD G 目标）
    chunkSizeWarningLimit: 250,
    reportCompressedSize: true,
    rollupOptions: {
      output: {
        // 手工分包：echarts 体积大且很少变，单独放一个 chunk 能长期缓存
        manualChunks(id) {
          if (id.includes('node_modules/echarts') || id.includes('node_modules/zrender')) {
            return 'echarts'
          }
          if (id.includes('node_modules/vue') || id.includes('node_modules/@vue')) {
            return 'vue-core'
          }
          return undefined
        },
      },
    },
  },

  server: {
    port: Number(process.env.VITE_DEV_PORT ?? 5173),
    host: '127.0.0.1',
    /*
     * 开发时把 API 转发到本地服务端，免得起前端还要先编译 Go。
     *
     * 目标地址走环境变量：联调脚本会用不同端口起后端，
     * 写死8000 就只能对着固定的进程调试。
     */
    proxy: {
      '/api': {
        target: process.env.VITE_DEV_PROXY_TARGET ?? 'http://127.0.0.1:8000',
        changeOrigin: true,
      },
      '/healthz': {
        target: process.env.VITE_DEV_PROXY_TARGET ?? 'http://127.0.0.1:8000',
        changeOrigin: true,
      },
      '/ready': {
        target: process.env.VITE_DEV_PROXY_TARGET ?? 'http://127.0.0.1:8000',
        changeOrigin: true,
      },
      // WebSocket：前端实时推送
      '/api/v1/ws': {
        target: process.env.VITE_DEV_PROXY_TARGET?.replace('http', 'ws') ?? 'ws://127.0.0.1:8000',
        ws: true,
      },
    },
  },
})
