import { defineAsyncComponent } from 'vue'

export {
  assertProductManifest,
  productModuleFor,
  productModules,
  productRoutesFor,
  type ProductIcon,
  type ProductHost,
  type ProductModule,
  type ProductModuleId,
  type ProductRoute
} from './manifest'
export {
  createProductRouter,
  createProductMemoryHistory,
  productBreadcrumbs,
  productHistoryMode,
  resolveAuthorizedProductRoutes,
  type ProductBreadcrumb,
  type ProductHistoryMode,
  type ProductRouterOptions
} from './router'
export type { SessionClient } from '@go-admin-plus/domain-iam/session'

export const ProductWorkspace = defineAsyncComponent(() => import('./ProductWorkspace.vue'))
