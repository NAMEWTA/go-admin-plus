<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { createDesktopFetch, createDesktopPlatform, createDesktopRuntime, createDesktopSessionClient, createDesktopTransport } from '@go-admin-plus/adapter-desktop'
import { ProductWorkspace } from '@go-admin-plus/app-shell/product'
import { ADMIN_THEME_STORAGE_KEY } from '@go-admin-plus/ui'

import FirstSetupGate from '../first-setup/FirstSetupGate.vue'

const runtime = createDesktopRuntime()
const platform = createDesktopPlatform()
const fetcher = createDesktopFetch()
const session = createDesktopSessionClient()
const nativeBoundary = ref('')
const nativeAuthorization = ref('')
const workspaceKey = ref(0)
const transport = createDesktopTransport()
let boundaryTimer: number | undefined
let nativeConfirm: typeof window.confirm | undefined
const nativeE2eRunStorageKey = 'go-admin-plus.native-e2e-run'
const nativeE2eRunId = import.meta.env.VITE_GO_ADMIN_NATIVE_E2E_RUN_ID

if (!nativeE2eRunId) throw new Error('native E2E run identity unavailable')
if (window.localStorage.getItem(nativeE2eRunStorageKey) !== nativeE2eRunId) {
  window.localStorage.clear()
  window.localStorage.setItem(nativeE2eRunStorageKey, nativeE2eRunId)
}

const themeStorageIsSafe = () => {
  const keys = Array.from({ length: window.localStorage.length }, (_, index) => window.localStorage.key(index))
  if (keys.some(key => key !== ADMIN_THEME_STORAGE_KEY && key !== nativeE2eRunStorageKey)) return false
  if (window.localStorage.getItem(nativeE2eRunStorageKey) !== nativeE2eRunId) return false
  const storedTheme = window.localStorage.getItem(ADMIN_THEME_STORAGE_KEY)
  if (storedTheme === null) return true
  try {
    const value = JSON.parse(storedTheme) as Record<string, unknown>
    return (value.preference === 'light' || value.preference === 'dark' || value.preference === 'system') &&
      (value.density === 'comfortable' || value.density === 'compact') && Object.keys(value).length === 2
  } catch {
    return false
  }
}

const verifyNativeBoundary = async () => {
  try {
    const text = document.body.textContent ?? ''
    const location = new URL(window.location.href)
    const databases = typeof window.indexedDB.databases === 'function' ? await window.indexedDB.databases() : []
    const cacheNames = 'caches' in window ? await window.caches.keys() : []
    const opaqueMaterial = text.split(/\s+/).some(value => /^[A-Za-z0-9_-]{43}$/.test(value) ||
      /^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}$/.test(value))
    const trustedLocation = location.protocol === 'tauri:' || location.hostname === 'tauri.localhost'
    const failures = [
      document.cookie !== '' ? 'cookie' : '',
      !themeStorageIsSafe() ? 'local-storage' : '',
      window.sessionStorage.length !== 0 ? 'session-storage' : '',
      databases.length !== 0 ? 'indexed-db' : '',
      cacheNames.length !== 0 ? 'cache-storage' : '',
      !trustedLocation ? 'location' : '',
      text.includes('__Host-go-admin-session') || text.includes('csrfToken') || text.includes('X-CSRF-Token') ? 'protected-text' : '',
      opaqueMaterial ? 'opaque-text' : ''
    ].filter(Boolean)
    const safe = failures.length === 0
    nativeBoundary.value = safe && text.includes('Administrator')
      ? 'E2E authenticated boundary verified'
      : safe && text.includes('使用管理员账号登录控制台') ? 'E2E unauthenticated boundary verified'
        : `E2E boundary blocked: ${failures.join(',')}`
  } catch {
    nativeBoundary.value = ''
  }
}

const clearThemeStorage = () => {
  window.localStorage.removeItem(ADMIN_THEME_STORAGE_KEY)
  window.localStorage.removeItem(nativeE2eRunStorageKey)
  nativeAuthorization.value = window.localStorage.length === 0
    ? 'E2E theme storage cleared'
    : 'E2E control failed: theme-storage-cleanup'
}

const openRoles = () => { window.location.hash = '#/iam/roles' }
const openFiles = () => {
  window.location.hash = '#/files'
}

const useDarkTheme = () => {
  const control = document.querySelector<HTMLButtonElement>('button[aria-label="使用深色主题"]')
  if (!control) {
    nativeAuthorization.value = 'E2E control failed: theme-dark'
    return
  }
  control.click()
}

const nativeControl = async (action: 'scope-self' | 'scope-all' | 'permissions-off' | 'permissions-on' | 'session-revoke') => {
  nativeAuthorization.value = ''
  let stage = 'control-request'
  try {
    const result = await transport.request('/__desktop/test-control', 'POST', { action })
    stage = 'control-response'
    if (result.status !== 204 || result.body !== null) throw new Error('native control failed')
    if (action === 'scope-self' || action === 'scope-all') {
      stage = 'scope-request'
      const response = await fetcher('/api/files/objects?page=1&pageSize=20&sort=name&direction=ascending')
      stage = 'scope-status'
      if (response.status !== 200) throw new Error('native scope request failed')
      stage = 'scope-body'
      const payload = await response.json() as { rows?: Array<{ originalName?: string }> }
      const foreignVisible = payload.rows?.some(row => row.originalName === 'foreign.txt') === true
      stage = 'scope-boundary'
      if (action === 'scope-self' ? foreignVisible : !foreignVisible) throw new Error('native scope boundary failed')
      nativeAuthorization.value = action === 'scope-self' ? 'E2E self scope enforced' : 'E2E all scope restored'
    }
    if (action === 'permissions-off') {
      stage = 'permission-request'
      const response = await fetcher('/api/files/objects?page=1&pageSize=20&sort=name&direction=ascending')
      stage = 'permission-status'
      if (response.status !== 403) throw new Error('native authorization remained active')
      nativeAuthorization.value = 'E2E authorization denied'
    }
    if (action === 'permissions-on') nativeAuthorization.value = 'E2E authorization restored'
    workspaceKey.value += 1
  } catch {
    nativeAuthorization.value = `E2E control failed: ${stage}`
  }
}

onMounted(() => {
  nativeConfirm = window.confirm
  window.confirm = () => true
  boundaryTimer = window.setInterval(() => { void verifyNativeBoundary() }, 100)
})
onUnmounted(() => {
  if (boundaryTimer !== undefined) window.clearInterval(boundaryTimer)
  if (nativeConfirm) window.confirm = nativeConfirm
})
</script>

<template>
  <FirstSetupGate v-slot="{ workspaceKey: setupKey }">
    <aside class="native-e2e" aria-live="polite">
      <span v-if="nativeBoundary">{{ nativeBoundary }}</span>
      <span v-if="nativeAuthorization">{{ nativeAuthorization }}</span>
      <button type="button" @click="openRoles">E2E open Roles</button>
      <button type="button" @click="openFiles">E2E open Files</button>
      <button type="button" @click="useDarkTheme">E2E use dark theme</button>
      <button type="button" @click="nativeControl('scope-self')">E2E scope self</button>
      <button type="button" @click="nativeControl('scope-all')">E2E scope all</button>
      <button type="button" @click="nativeControl('permissions-off')">E2E permissions off</button>
      <button type="button" @click="nativeControl('permissions-on')">E2E permissions on</button>
      <button type="button" @click="nativeControl('session-revoke')">E2E revoke session</button>
      <button type="button" @click="clearThemeStorage">E2E reset theme</button>
    </aside>
    <ProductWorkspace :key="`${setupKey}-${workspaceKey}`" host="desktop" :runtime="runtime" :platform="platform" :fetcher="fetcher" :session-client="session" />
  </FirstSetupGate>
</template>

<style scoped>
.native-e2e { position: fixed; z-index: 10; right: 8px; bottom: 8px; display: flex; max-width: calc(100vw - 16px); flex-wrap: wrap; gap: 4px; padding: 6px; background: #fff; border: 1px solid #9ba6aa; }
.native-e2e span { width: 100%; font-size: 12px; }
.native-e2e button { min-height: 28px; padding: 2px 6px; font-size: 11px; }
</style>
