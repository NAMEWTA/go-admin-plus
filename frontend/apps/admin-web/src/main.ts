import { createApp } from 'vue'

import { createBrowserSessionFetch, createWebRuntime } from '@go-admin-plus/adapter-browser'
import { createProductRouter } from '@go-admin-plus/app-shell/product'

import App from './App.vue'
import './styles.css'
import '@go-admin-plus/ui/admin-theme.css'

const fetcher = createBrowserSessionFetch()
const runtime = createWebRuntime(fetcher)
const router = createProductRouter('web', runtime)

createApp(App, { fetcher, runtime }).use(router).mount('#app')
