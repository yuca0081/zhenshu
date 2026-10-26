import { createApp } from 'vue'
import App from './App.vue'

const app = createApp(App)
// 生产构建 Vue 会静默吞掉组件渲染错误（warn 被剥离）——白屏无报错的元凶。
// 统一落 console.error：现场可截图排查，也不会再无声消失。
app.config.errorHandler = (err, _inst, info) => {
  console.error(`[vue] ${info}:`, err)
}
// naive-ui 不做全量注册：各组件已局部 import（createDiscreteApi 自带
// configProviderProps），app.use(naive) 会让全量 install 打败 tree-shaking，
// 实测 bundle 1.6MB——删掉后按需打包。
app.mount('#app')
