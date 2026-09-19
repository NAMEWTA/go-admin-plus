import { defineComponent, h, type Component } from 'vue'
import {
  createRouter,
  createMemoryHistory,
  createWebHashHistory,
  createWebHistory,
  type RouteLocationNormalizedLoaded,
  type Router,
  type RouterHistory
} from 'vue-router'

import type { NavigationEntry, RuntimeIdentity, ShellRuntimePort } from '@go-admin-plus/platform'

import { productModuleFor, productRoutesFor, type ProductHost, type ProductRoute } from './manifest'

export type ProductHistoryMode = 'html5' | 'hash'

export interface ProductRouterOptions {
  readonly history?: RouterHistory
  readonly documentTitle?: string
}

export interface ProductBreadcrumb {
  readonly title: string
  readonly path?: string
}

const ShellRouteOutlet = defineComponent({
  name: 'ShellRouteOutlet',
  setup: () => () => h('span', { 'aria-hidden': 'true' })
})

const routeLoadFailure = (route: ProductRoute): Component => defineComponent({
  name: 'RouteLoadFailure',
  setup: () => () => h('section', { class: 'product-shell__route-error', role: 'alert' }, [
    h('h1', '页面加载失败'),
    h('p', { class: 'product-shell__trace' }, `Trace: route:${route.name}`),
    h('button', { type: 'button', onClick: () => window.location.reload() }, '重新加载')
  ])
})

const loadRoute = (route: ProductRoute) => async (): Promise<Component> => {
  try {
    return await route.component()
  } catch {
    return routeLoadFailure(route)
  }
}

export const productHistoryMode = (host: ProductHost): ProductHistoryMode =>
  host === 'desktop' ? 'hash' : 'html5'

export const createProductMemoryHistory = (): RouterHistory => createMemoryHistory()

export const resolveAuthorizedProductRoutes = (
  host: ProductHost,
  identity: Extract<RuntimeIdentity, { kind: 'authenticated' }>,
  navigation: ReadonlyArray<NavigationEntry>
): readonly ProductRoute[] => {
  const serverPaths = new Map(navigation.filter(entry => entry.path).map(entry => [entry.path, entry]))
  const grants = new Set<string>(identity.permissions)
  return productRoutesFor(host).filter(route => {
    const entry = serverPaths.get(route.path)
    return entry && (!entry.routeKey || entry.routeKey === route.name) && grants.has(route.permission)
  }).map(route => {
    const entry = serverPaths.get(route.path)!
    return { ...route, title: entry.label || route.title, order: entry.sortOrder ?? route.order }
  }).sort((a,b) => a.order - b.order)
}

export const productBreadcrumbs = (
  host: ProductHost,
  route: RouteLocationNormalizedLoaded
): readonly ProductBreadcrumb[] => {
  if (route.name === 'account') return [{ title: '工作台', path: '/' }, { title: '个人中心' }]
  const productRoute = productRoutesFor(host).find(candidate => candidate.name === route.name)
  if (!productRoute) return [{ title: '工作台' }]
  return [
    { title: '工作台', path: '/' },
    { title: productModuleFor(productRoute.module).title },
    { title: productRoute.title }
  ]
}

export const createProductRouter = (
  host: ProductHost,
  runtime: ShellRuntimePort,
  options: ProductRouterOptions = {}
): Router => {
  const compiledRoutes = productRoutesFor(host)
  const history = options.history ?? (host === 'desktop' ? createWebHashHistory() : createWebHistory())
  const router = createRouter({
    history,
    routes: [
      { path: '/', name: 'workspace-root', component: ShellRouteOutlet },
      { path: '/login', name: 'login', component: ShellRouteOutlet },
      { path: '/account', name: 'account', component: ShellRouteOutlet },
      { path: '/forbidden', name: 'forbidden', component: ShellRouteOutlet },
      { path: '/unavailable', name: 'unavailable', component: ShellRouteOutlet },
      ...compiledRoutes.map(route => ({
        path: route.path,
        name: route.name,
        component: loadRoute(route),
        meta: { productRoute: route }
      })),
      { path: '/:pathMatch(.*)*', name: 'not-found', component: ShellRouteOutlet }
    ]
  })

  router.beforeEach(async to => {
    if (to.name === 'login' || to.name === 'not-found' || to.name === 'forbidden' || to.name === 'unavailable') return true
    try {
      const identity = await runtime.loadIdentity()
      if (identity.kind === 'unauthenticated') {
        return to.name === 'login' ? true : { name: 'login', replace: true }
      }
      if (to.name === 'account') return true

      const navigation = await runtime.loadNavigation()
      const allowed = resolveAuthorizedProductRoutes(host, identity, navigation)
      if (to.name === 'workspace-root') {
        return allowed[0] ? { path: allowed[0].path, replace: true } : { name: 'forbidden', replace: true }
      }
      return allowed.some(route => route.name === to.name)
        ? true
        : { name: 'forbidden', replace: true }
    } catch {
      return { name: 'unavailable', replace: true }
    }
  })

  router.afterEach(to => {
    if (typeof document === 'undefined') return
    const productRoute = compiledRoutes.find(candidate => candidate.name === to.name)
    const title = productRoute?.title
      ?? (to.name === 'account' ? '个人中心' : to.name === 'forbidden' ? '无权访问' : to.name === 'not-found' ? '页面不存在' : to.name === 'unavailable' ? '服务暂不可用' : '工作台')
    document.title = `${title} - ${options.documentTitle ?? 'Go Admin Plus'}`
  })

  return router
}
