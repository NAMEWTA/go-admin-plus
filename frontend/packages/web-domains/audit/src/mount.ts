/// <reference lib="dom" />

import { createApp, type Component } from 'vue'
import AuditPage from './AuditPage.vue'
import type { AuditController } from './audit-controller'

export const mountAuditPage = (element: Element | string, controller: AuditController) =>
  createApp(AuditPage as Component, { controller }).mount(element)
