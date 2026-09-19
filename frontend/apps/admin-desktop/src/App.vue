<script setup lang="ts">
import { createDesktopPlatform } from '@go-admin-plus/adapter-desktop'
import { ProductWorkspace, type SessionClient } from '@go-admin-plus/app-shell/product'
import type { ShellRuntimePort } from '@go-admin-plus/platform'

import FirstSetupGate from './first-setup/FirstSetupGate.vue'
import ConnectionGate from './ConnectionGate.vue'

defineProps<{
  runtime: ShellRuntimePort
  fetcher: typeof globalThis.fetch
  session: SessionClient
}>()
const platform = createDesktopPlatform()
</script>

<template>
  <ConnectionGate><FirstSetupGate v-slot="{ workspaceKey }">
    <ProductWorkspace :key="workspaceKey" host="desktop" :runtime="runtime" :platform="platform" :fetcher="fetcher" :session-client="session" />
  </FirstSetupGate></ConnectionGate>
</template>
