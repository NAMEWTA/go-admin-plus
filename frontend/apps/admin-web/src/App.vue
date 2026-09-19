<script setup lang="ts">
import { createWebPlatform } from '@go-admin-plus/adapter-browser'
import { ProductWorkspace } from '@go-admin-plus/app-shell/product'
import type { ShellRuntimePort } from '@go-admin-plus/platform'

defineProps<{
  runtime: ShellRuntimePort
  fetcher: typeof globalThis.fetch
}>()
const platform = createWebPlatform()
</script>

<template>
  <ProductWorkspace host="web" :runtime="runtime" :platform="platform" :fetcher="fetcher" />
</template>
