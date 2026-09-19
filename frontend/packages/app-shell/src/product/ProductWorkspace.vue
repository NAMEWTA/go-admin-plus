<script setup lang="ts">
import NavigationTree from './NavigationTree.vue'
import type { NavigationEntry } from '@go-admin-plus/platform'
import {
  CalendarClockIcon,
  ChevronDownIcon,
  FileStackIcon,
  FolderOpenIcon,
  HomeIcon,
  LayoutDashboardIcon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  PackageIcon,
  PanelLeftCloseIcon,
  PanelLeftOpenIcon,
  RefreshCwIcon,
  ScrollTextIcon,
  ShieldCheckIcon,
  SunIcon,
  UserRoundIcon,
  XIcon
} from '@lucide/vue'
import { computed, onMounted, onUnmounted, ref, shallowRef, watch, type Component } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'

import type { AccountProfile, SessionClient, SessionState } from '@go-admin-plus/domain-iam/session'
import { createSessionController } from '@go-admin-plus/domain-iam/session'
import type { PlatformPort, ShellRuntimePort } from '@go-admin-plus/platform'
import { createSessionAwareFetch } from '@go-admin-plus/ui'
import { createThemeController } from '@go-admin-plus/ui'
import type { ThemePreference } from '@go-admin-plus/ui'
import { createAuditController, createWebAuditClient } from '@go-admin-plus/web-domain-audit'
import { createFilesController, createWebFilesClient } from '@go-admin-plus/web-domain-files'
import { AccountPage, createWebSessionClient, LoginPage } from '@go-admin-plus/web-domain-iam/session'
import { createAdministrationController, createWebAdministrationClient } from '@go-admin-plus/web-domain-iam/administration'
import { createSchedulerController, createWebSchedulerClient } from '@go-admin-plus/web-domain-scheduler'

import { productModuleFor, productRoutesFor, type ProductHost, type ProductRoute } from './manifest'
import { productBreadcrumbs, resolveAuthorizedProductRoutes } from './router'

const props = defineProps<{
  host: ProductHost
  runtime: ShellRuntimePort
  platform: PlatformPort
  fetcher?: typeof globalThis.fetch
  sessionClient?: SessionClient
}>()

type View = 'loading' | 'login' | 'workspace' | 'account' | 'forbidden' | 'not-found' | 'unavailable'

const rawFetcher = props.fetcher ?? globalThis.fetch
// One session boundary is shared by every web-domain client in this workspace.
const fetcher = props.host === 'web' ? createSessionAwareFetch(rawFetcher) : rawFetcher
const platform = props.platform
const currentRoute = useRoute()
const router = useRouter()
const session = createSessionController(props.sessionClient ?? createWebSessionClient(fetcher))
const sessionState = ref<SessionState>(session.state())
const permissions = ref<ReadonlySet<string>>(new Set())
const dataScope = ref<'self' | 'all' | null>(null)
const navigationEntries = ref<ReadonlyArray<NavigationEntry>>([])
const navigationPaths = ref<ReadonlySet<string>>(new Set())
const view = ref<View>('loading')
const sidebarCollapsed = ref(false)
const mobileNavigationOpen = ref(false)
const accountMenuOpen = ref(false)
const visitedPaths = ref<string[]>([])
const theme = createThemeController()
const themeSnapshot = ref(theme.snapshot())
let sessionGeneration = 0
let runtimeAbort: AbortController | undefined
const beginGeneration = () => {
  runtimeAbort?.abort()
  runtimeAbort = new AbortController()
  sessionGeneration += 1
  return { generation: sessionGeneration, signal: runtimeAbort.signal }
}
const currentGeneration = (generation: number, signal: AbortSignal) => generation === sessionGeneration && !signal.aborted

const capability = {
  can: (permission: string) => permissions.value.has(permission),
  scope: () => dataScope.value
}
const confirmRemoval = async (count: number) => window.confirm(count === 1 ? '确定删除该记录吗？' : `确定删除所选的 ${count} 条记录吗？`)
const confirmAuditCleanup = async () => window.confirm('确定清理所选日期之前且符合保留策略的审计日志吗？')

// 每次切换账号重建业务控制器，并取消旧账号未完成的请求，避免列表/弹窗状态跨会话残留。
let businessAbort = new AbortController()
const businessGeneration = ref(0)
const createControllers = () => {
  const signal = businessAbort.signal
  const scopedFetch: typeof fetch = (input, init) => {
    const request = new Request(input, init)
    return fetcher(new Request(request, { signal: AbortSignal.any([signal, request.signal]) }))
  }
  Object.defineProperty(scopedFetch, Symbol.for('go-admin-plus.session-fetch'), { value: true })
  return {
    administration: createAdministrationController(createWebAdministrationClient(scopedFetch), confirmRemoval),
    audit: createAuditController(createWebAuditClient(scopedFetch), confirmAuditCleanup),
    scheduler: createSchedulerController(createWebSchedulerClient(scopedFetch), capability, confirmRemoval),
    files: createFilesController(createWebFilesClient(scopedFetch), confirmRemoval, capability)
  }
}
const controllers = shallowRef(createControllers())
const clearBusinessState = () => {
  businessAbort.abort()
  businessAbort = new AbortController()
  controllers.value = createControllers()
  businessGeneration.value += 1
  visitedPaths.value = []
  accountMenuOpen.value = false
  mobileNavigationOpen.value = false
}

const routes = computed(() => resolveAuthorizedProductRoutes(props.host, {kind:'authenticated',subjectId:profile.value?.id ?? '',permissions:[...permissions.value] as `${string}.${string}`[],dataScope:dataScope.value ?? 'self'},navigationEntries.value))
const profile = computed<AccountProfile | null>(() => sessionState.value.profile)
const routePath = computed(() => currentRoute.path)
const productRoute = computed(() => productRoutesFor(props.host).find(candidate => candidate.name === currentRoute.name))
const breadcrumbs = computed(() => productBreadcrumbs(props.host, currentRoute))
const moduleIcons: Readonly<Record<string, Component>> = {
  iam: ShieldCheckIcon,
  audit: ScrollTextIcon,
  scheduler: CalendarClockIcon,
  files: FolderOpenIcon
}
const navigationIcon = (route: ProductRoute) => moduleIcons[route.module] ?? LayoutDashboardIcon
const routeGroups = computed(() => {
  const groups = new Map<string, { key: string; label: string; icon: Component; routes: typeof routes.value }>()
  for (const item of routes.value) {
    const key = item.module
    const existing = groups.get(key)
    if (existing) existing.routes = [...existing.routes, item]
    else groups.set(key, { key, label: productModuleFor(item.module).title, icon: navigationIcon(item), routes: [item] })
  }
  return [...groups.values()]
})
const visitedRoutes = computed(() => visitedPaths.value
  .map(visited => productRoutesFor(props.host).find(candidate => candidate.path === visited))
  .filter(candidate => candidate !== undefined))
const profileMark = computed(() => (profile.value?.displayName.trim().charAt(0) || 'A').toUpperCase())
const module = computed(() => productRoute.value?.module ?? null)
const routeComponentProps = computed(() => {
  switch (module.value) {
    case 'iam': return { controller: controllers.value.administration }
    case 'audit': return { controller: controllers.value.audit }
    case 'scheduler': return { controller: controllers.value.scheduler }
    case 'files': return { controller: controllers.value.files, platform }
    default: return {}
  }
})

const replacePath = (next: string) => {
  void router.replace(next)
}
const setThemePreference = (preference: ThemePreference) => {
  theme.setPreference(preference)
  themeSnapshot.value = theme.snapshot()
}
const rememberRoute = (next: string) => {
  if (!productRoutesFor(props.host).some(candidate => candidate.path === next)) return
  visitedPaths.value = [...visitedPaths.value.filter(visited => visited !== next), next]
}
const navigate = (next: string) => {
  void router.push(next)
  rememberRoute(next)
  mobileNavigationOpen.value = false
  accountMenuOpen.value = false
  resolveView()
}
const resolveView = (authorizedRoutes: readonly ProductRoute[] = routes.value) => {
  if (sessionState.value.status !== 'authenticated') { view.value = 'login'; return }
  if (currentRoute.name === 'unavailable') { view.value = 'unavailable'; return }
  if (currentRoute.name === 'forbidden') { view.value = 'forbidden'; return }
  if (currentRoute.name === 'not-found') { view.value = 'not-found'; return }
  if (routePath.value === '/account') { view.value = 'account'; return }
  if (routePath.value === '/' || routePath.value === '/login') {
    const first = authorizedRoutes[0]?.path
    if (first) { replacePath(first); rememberRoute(first); view.value = 'workspace' } else view.value = 'forbidden'
    return
  }
  if (!productRoute.value) { view.value = 'not-found'; return }
  if (authorizedRoutes.some(candidate => candidate.path === routePath.value)) {
    rememberRoute(routePath.value)
    view.value = 'workspace'
  } else {
    replacePath('/forbidden')
    view.value = 'forbidden'
  }
}
const loadRuntime = async () => {
  const request = beginGeneration()
  view.value = 'loading'
  try {
    const identity = await props.runtime.loadIdentity({ signal: request.signal })
    if (!currentGeneration(request.generation, request.signal)) return
    if (identity.kind === 'unauthenticated') {
      permissions.value = new Set()
      dataScope.value = null
      navigationPaths.value = new Set(); navigationEntries.value=[]
      view.value = 'login'
      return
    }
    permissions.value = new Set(identity.permissions)
    dataScope.value = identity.dataScope
    const navigation = await props.runtime.loadNavigation({ signal: request.signal })
    if (!currentGeneration(request.generation, request.signal)) return
    navigationEntries.value = navigation
    navigationPaths.value = new Set(navigation.map(entry => entry.path))
    const authorizedRoutes = resolveAuthorizedProductRoutes(props.host, identity, navigation)
    const reachable = new Set<string>(authorizedRoutes.map(route => route.path))
    visitedPaths.value = visitedPaths.value.filter(visited => reachable.has(visited))
    resolveView(authorizedRoutes)
  } catch (error) {
    if (!currentGeneration(request.generation, request.signal) || (error instanceof DOMException && error.name === 'AbortError')) return
    view.value = 'unavailable'
  }
}
const authenticated = async () => { await loadRuntime() }
const requireSession = () => {
  clearBusinessState()
  sessionGeneration += 1
  runtimeAbort?.abort()
  runtimeAbort = undefined
  permissions.value = new Set()
  dataScope.value = null
  navigationPaths.value = new Set(); navigationEntries.value=[]
  replacePath('/login')
  view.value = 'login'
}
const forbid = () => { replacePath('/forbidden'); view.value = 'forbidden' }
const signedOut = () => requireSession()
const signOut = async () => {
  accountMenuOpen.value = false
  if (!window.confirm('确定注销并退出系统吗？')) return
  await session.logout()
  if (session.state().status === 'unauthenticated') signedOut()
}
const closeTag = (target: string) => {
  if (visitedPaths.value.length <= 1) return
  const index = visitedPaths.value.indexOf(target)
  visitedPaths.value = visitedPaths.value.filter(visited => visited !== target)
  if (target !== routePath.value) return
  const fallback = visitedPaths.value[Math.min(index, visitedPaths.value.length - 1)] ?? routes.value[0]?.path
  if (fallback) navigate(fallback)
}
const restore = async () => {
  sessionGeneration += 1
  runtimeAbort?.abort()
  runtimeAbort = undefined
  view.value = 'loading'
  await session.restore()
  if (session.state().status === 'authenticated') await loadRuntime()
  else view.value = 'login'
}
const unsubscribe = session.subscribe(state => {
  if (state.profile?.id !== sessionState.value.profile?.id) clearBusinessState()
  sessionState.value = state
  if (state.status === 'unauthenticated' && view.value !== 'loading') requireSession()
})

onMounted(() => {
  void restore()
})
onUnmounted(() => {
  businessAbort.abort()
  sessionGeneration += 1
  runtimeAbort?.abort()
  runtimeAbort = undefined
  unsubscribe()
  if (props.host === 'web') (fetcher as { close?: () => void }).close?.()
  theme.destroy()
})
watch(() => currentRoute.fullPath, () => {
  rememberRoute(routePath.value)
  if (currentRoute.name === 'forbidden') { view.value = 'forbidden'; return }
  if (currentRoute.name === 'not-found') { view.value = 'not-found'; return }
  if (currentRoute.name === 'unavailable') { view.value = 'unavailable'; return }
  if (sessionState.value.status === 'authenticated' && view.value !== 'loading' && view.value !== 'login') resolveView()
})
</script>

<template>
  <main class="product-shell" :data-host="host" :data-shell-state="view">
    <section v-if="view === 'loading'" class="product-shell__state" aria-live="polite">
      <span class="product-shell__spinner" aria-hidden="true" />
      <p>正在加载</p>
    </section>

    <LoginPage v-else-if="view === 'login'" :controller="session" @authenticated="authenticated" />

    <section v-else-if="view === 'workspace' || view === 'account'" class="product-shell__workspace" :class="{ 'is-collapsed': sidebarCollapsed }">
      <button v-if="mobileNavigationOpen" class="product-shell__drawer-backdrop" type="button" aria-label="关闭导航" @click="mobileNavigationOpen = false" />
      <aside class="product-shell__sidebar" :class="{ 'is-mobile-open': mobileNavigationOpen }">
        <button class="product-shell__brand" type="button" title="Go Admin Plus" @click="navigate('/')">
          <span class="product-shell__brand-mark"><ShieldCheckIcon :size="20" aria-hidden="true" /></span>
          <span class="product-shell__brand-copy"><strong class="product-shell__brand-name">Go Admin Plus</strong><small>管理控制台</small></span>
        </button>
        <nav class="product-shell__navigation" aria-label="主导航">
          <NavigationTree :entries="navigationEntries" :routes="routes" :active-path="routePath" @navigate="navigate" />
        </nav>
      </aside>

      <div class="product-shell__main">
        <header class="product-shell__header">
          <button class="product-shell__hamburger" type="button" aria-label="切换导航" @click="mobileNavigationOpen = !mobileNavigationOpen; sidebarCollapsed = !sidebarCollapsed">
            <PanelLeftOpenIcon v-if="sidebarCollapsed" :size="19" aria-hidden="true" />
            <PanelLeftCloseIcon v-else :size="19" aria-hidden="true" />
          </button>
          <nav class="product-shell__breadcrumb" aria-label="面包屑">
            <HomeIcon :size="15" aria-hidden="true" />
            <template v-for="(crumb, index) in breadcrumbs" :key="`${crumb.title}-${index}`">
              <button v-if="crumb.path" type="button" @click="navigate(crumb.path)">{{ crumb.title }}</button>
              <strong v-else>{{ crumb.title }}</strong>
              <span v-if="index < breadcrumbs.length - 1" aria-hidden="true">/</span>
            </template>
          </nav>
          <div class="product-shell__identity">
            <div class="product-shell__theme" role="group" aria-label="主题模式">
              <button
                type="button"
                :aria-label="themeSnapshot.preference === 'system' ? '当前跟随系统主题' : '跟随系统主题'"
                :aria-pressed="themeSnapshot.preference === 'system'"
                title="跟随系统主题"
                @click="setThemePreference('system')"
              ><MonitorIcon :size="16" aria-hidden="true" /></button>
              <button
                type="button"
                :aria-label="themeSnapshot.preference === 'light' ? '当前使用浅色主题' : '使用浅色主题'"
                :aria-pressed="themeSnapshot.preference === 'light'"
                title="使用浅色主题"
                @click="setThemePreference('light')"
              ><SunIcon :size="16" aria-hidden="true" /></button>
              <button
                type="button"
                :aria-label="themeSnapshot.preference === 'dark' ? '当前使用深色主题' : '使用深色主题'"
                :aria-pressed="themeSnapshot.preference === 'dark'"
                title="使用深色主题"
                @click="setThemePreference('dark')"
              ><MoonIcon :size="16" aria-hidden="true" /></button>
            </div>
            <span class="product-shell__host">{{ host === 'desktop' ? 'Desktop' : 'Web' }}</span>
            <button v-if="profile" class="product-shell__profile" type="button" aria-label="账户菜单" :aria-expanded="accountMenuOpen" @click="accountMenuOpen = !accountMenuOpen">
              <span class="product-shell__avatar">{{ profileMark }}</span><span>{{ profile.displayName }}</span><ChevronDownIcon :size="14" aria-hidden="true" />
            </button>
            <div v-if="accountMenuOpen" class="product-shell__account-menu">
              <button type="button" @click="navigate('/account')"><UserRoundIcon :size="15" aria-hidden="true" />个人中心</button>
              <button type="button" @click="signOut"><LogOutIcon :size="15" aria-hidden="true" />退出登录</button>
            </div>
          </div>
        </header>

        <div class="product-shell__tags" aria-label="已访问页面">
          <div v-for="item in visitedRoutes" :key="item.path" class="product-shell__tag" :class="{ 'is-active': routePath === item.path }">
            <button type="button" :aria-current="routePath === item.path ? 'page' : undefined" @click="navigate(item.path)">{{ item.title }}</button>
            <button v-if="visitedRoutes.length > 1" class="product-shell__tag-close" type="button" :aria-label="`关闭 ${item.title}`" @click="closeTag(item.path)"><XIcon :size="12" aria-hidden="true" /></button>
          </div>
        </div>

        <div class="product-shell__content">
          <RouterView v-if="view === 'workspace'" v-slot="{ Component }">
            <Suspense>
              <component
                :is="Component"
                :key="`${businessGeneration}:${routePath}`"
                v-bind="routeComponentProps"
                @session-required="requireSession"
                @relogin="requireSession"
                @forbidden="forbid"
              />
              <template #fallback><p class="product-shell__route-loading" aria-live="polite">正在加载页面</p></template>
            </Suspense>
          </RouterView>
          <AccountPage v-else-if="view === 'account' && profile" :controller="session" :profile="profile" @signed-out="signedOut" />
        </div>
      </div>
    </section>

    <section v-else class="product-shell__state">
      <FileStackIcon class="product-shell__state-icon" :size="34" aria-hidden="true" />
      <p class="product-shell__code">{{ view === 'forbidden' ? '403' : view === 'not-found' ? '404' : 'RUNTIME' }}</p>
      <h1>{{ view === 'forbidden' ? '无权访问' : view === 'not-found' ? '页面不存在' : '服务暂不可用' }}</h1>
      <button type="button" @click="view === 'unavailable' ? restore() : navigate('/')"><RefreshCwIcon v-if="view === 'unavailable'" :size="16" aria-hidden="true" />{{ view === 'unavailable' ? '重试' : '返回工作台' }}</button>
    </section>
  </main>
</template>

<style scoped src="./ProductWorkspace.scss"></style>
