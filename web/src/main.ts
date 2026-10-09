import { createApp } from 'vue'
import naive from 'naive-ui'
import App from './App.vue'

const app = createApp(App)
// 生产构建 Vue 会静默吞掉组件渲染错误（warn 被剥离）——白屏无报错的元凶。
// 统一落 console.error：现场可截图排查，也不会再无声消失。
app.config.errorHandler = (err, _inst, info) => {
  // eslint-disable-next-line no-console
  console.error(`[vue] ${info}:`, err)
}
app.use(naive).mount('#app')
