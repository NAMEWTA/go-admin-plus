<script setup lang="ts">
import { computed } from 'vue'
import type { NavigationEntry } from '@go-admin-plus/platform'
import type { ProductRoute } from './manifest'

const props = withDefaults(defineProps<{
  entries: ReadonlyArray<NavigationEntry>
  routes: ReadonlyArray<ProductRoute>
  parentId?: string
  activePath: string
  depth?: number
}>(), { parentId: '', depth: 0 })
const emit = defineEmits<{ navigate: [path: string] }>()

// 后端提供树与展示顺序，前端只为已注册且已授权的页面生成入口。
const children = computed(() => props.depth >= 16 ? [] : props.entries
  .filter(entry => (entry.parentId ?? '') === props.parentId &&
    (entry.kind === 'directory' || props.routes.some(route => route.path === entry.path)))
  .toSorted((left, right) => (left.sortOrder ?? 0) - (right.sortOrder ?? 0)))
const title = (entry: NavigationEntry) => entry.label || props.routes.find(route => route.path === entry.path)?.title || ''
</script>

<template>
  <ul class="navigation-tree">
    <li v-for="entry in children" :key="entry.id || entry.path">
      <details v-if="entry.kind === 'directory'" open>
        <summary>{{ entry.label }}</summary>
        <NavigationTree
          :entries="entries" :routes="routes" :parent-id="entry.id"
          :active-path="activePath" :depth="depth + 1" @navigate="emit('navigate', $event)"
        />
      </details>
      <button v-else type="button" :aria-current="entry.path === activePath ? 'page' : undefined"
        :title="title(entry)" @click="emit('navigate', entry.path)">
        {{ title(entry) }}
      </button>
    </li>
  </ul>
</template>

<style scoped>
.navigation-tree { list-style: none; margin: 0; padding: 0; }
.navigation-tree .navigation-tree { padding-left: 12px; }
summary { padding: 10px 14px; cursor: pointer; font-size: 13px; color: var(--ga-text-2); }
</style>
