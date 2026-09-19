import { createApp } from 'vue'

import { createDesktopFetch, createDesktopRuntime, createDesktopSessionClient } from '@go-admin-plus/adapter-desktop'
import { createProductRouter } from '@go-admin-plus/app-shell/product'

import App from '@desktop-entry'
import './styles.css'
import '@go-admin-plus/ui/admin-theme.css'

const runtime = createDesktopRuntime()
const fetcher = createDesktopFetch()
const session = createDesktopSessionClient()
const router = createProductRouter('desktop', runtime)

createApp(App, { fetcher, runtime, session }).use(router).mount('#app')
