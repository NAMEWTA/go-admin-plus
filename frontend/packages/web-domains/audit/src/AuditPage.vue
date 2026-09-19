<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { AuditRequestError, type AuditFact, type AuditFailure, type AuditFilters } from '@go-admin-plus/domain-audit'
import { consumeCleanupFailure, type AuditController } from './audit-controller'

const props = defineProps<{ controller: AuditController }>()
const emit = defineEmits<{ relogin: [] }>()
const version = ref(0)
const filters = reactive({ kind: '', action: '', outcome: '', source: '', from: '', to: '' })
const cleanupBefore = ref('')
const selected = ref<AuditFact | null>(null)
const detailDialog = ref<HTMLElement | null>(null)
const detailTrigger = ref<HTMLButtonElement | null>(null)
const failure = ref<AuditFailure | null>(null)
const failureReference = ref<string | null>(null)
const busy = ref(false)
const cleanupStatus = ref<'completed' | 'refresh-failed' | 'repair-required' | null>(null)
const snapshot = computed(() => { void version.value; return props.controller.list.snapshot() })
const run = async (action: () => Promise<unknown>) => {
	if (busy.value) return
	busy.value = true
	failure.value = null
	failureReference.value = null
	try {
		await action()
	} catch (error) {
		failure.value = error instanceof AuditRequestError ? error.category : 'unavailable'
		failureReference.value = error instanceof AuditRequestError ? error.traceId ?? null : null
		if (failure.value === 'relogin') emit('relogin')
	} finally {
		busy.value = false
		version.value += 1
	}
}
const asFilters = (): AuditFilters => ({
	...(filters.kind ? { kind: filters.kind as AuditFilters['kind'] } : {}),
	...(filters.action ? { action: filters.action as AuditFilters['action'] } : {}),
	...(filters.outcome ? { outcome: filters.outcome as AuditFilters['outcome'] } : {}),
	...(filters.source ? { source: filters.source as AuditFilters['source'] } : {}),
	...(filters.from ? { from: new Date(filters.from).toISOString() } : {}),
	...(filters.to ? { to: new Date(filters.to).toISOString() } : {}),
})
const search = () => run(() => props.controller.list.search(asFilters()))
const reset = async () => {
	Object.assign(filters, { kind: '', action: '', outcome: '', source: '', from: '', to: '' })
	await run(() => props.controller.list.reset())
}
const detail = (id: string, event: Event) => {
	detailTrigger.value = event.currentTarget as HTMLButtonElement
	return run(async () => {
		selected.value = await props.controller.detail(id)
		await nextTick()
		detailDialog.value?.focus()
	})
}
const closeDetail = async () => {
	selected.value = null
	await nextTick()
	detailTrigger.value?.focus()
}
const consumeRefreshFailure = () => {
	failure.value = consumeCleanupFailure(props.controller, () => emit('relogin'))
	failureReference.value = props.controller.lastTraceId()
}
const cleanup = () => run(async () => {
	cleanupStatus.value = null
	const result = await props.controller.cleanup(new Date(`${cleanupBefore.value}T00:00:00Z`).toISOString())
	if (result === 'completed' || result === 'refresh-failed' || result === 'repair-required') cleanupStatus.value = result
	if (result === 'failed') { failure.value = consumeCleanupFailure(props.controller, () => emit('relogin')); failureReference.value = props.controller.lastTraceId() }
	if (result === 'refresh-failed') consumeRefreshFailure()
})
const repairCleanup = () => run(async () => {
	const result = await props.controller.repairCleanup()
	if (result === 'completed' || result === 'refresh-failed') cleanupStatus.value = result
	if (result === 'refresh-failed') consumeRefreshFailure()
})
const formatDate = (value: string) => new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
const kindLabel = (value: AuditFact['kind']) => value === 'login' ? '登录日志' : '操作日志'
const actionLabel = (value: AuditFact['action']) => ({ login: '登录', create: '新增', update: '修改', delete: '删除' })[value]
const outcomeLabel = (value: AuditFact['outcome']) => value === 'succeeded' ? '成功' : '失败'
const sourceLabel = (value: AuditFact['source']) => ({ web: 'Web', desktop: '桌面端', server: '服务端' })[value]
onMounted(() => { void run(() => props.controller.list.refresh()) })
</script>

<template>
  <main class="audit-page ga-work-page">
    <header><h1>审计日志</h1></header>
    <form class="filters" aria-label="审计筛选" @submit.prevent="search">
      <label>日志类型<select v-model="filters.kind"><option value="">全部</option><option value="login">登录日志</option><option value="operation">操作日志</option></select></label>
      <label>操作<select v-model="filters.action" data-testid="audit-action"><option value="">全部</option><option value="login">登录</option><option value="create">新增</option><option value="update">修改</option><option value="delete">删除</option></select></label>
      <label>结果<select v-model="filters.outcome"><option value="">全部</option><option value="succeeded">成功</option><option value="failed">失败</option></select></label>
      <label>来源<select v-model="filters.source" data-testid="audit-source"><option value="">全部</option><option value="web">Web</option><option value="desktop">桌面端</option><option value="server">服务端</option></select></label>
      <label>开始时间<input v-model="filters.from" type="datetime-local"></label>
      <label>结束时间<input v-model="filters.to" type="datetime-local"></label>
      <div class="commands"><button type="submit" data-testid="audit-search" :disabled="busy">搜索</button><button type="button" :disabled="busy" @click="reset">重置</button></div>
    </form>
    <p v-if="failure === 'relogin'" role="alert">会话已失效，请重新登录。</p>
    <p v-else-if="failure === 'forbidden'" role="alert">没有执行该审计操作的权限。</p>
    <p v-else-if="failure" role="alert">审计服务暂不可用。</p>
    <p v-if="failureReference" class="problem-reference">参考编号：{{ failureReference }}</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>时间</th><th>类型</th><th>操作</th><th>结果</th><th>对象</th><th>来源</th><th>操作</th></tr></thead>
        <tbody>
					<tr v-for="fact in snapshot.rows" :key="fact.id" data-testid="audit-row">
						<td>{{ formatDate(fact.occurredAt) }}</td><td>{{ kindLabel(fact.kind) }}</td><td>{{ actionLabel(fact.action) }}</td><td>{{ outcomeLabel(fact.outcome) }}</td><td class="long-text">{{ fact.subject }}</td><td>{{ sourceLabel(fact.source) }}</td>
              <td><button type="button" data-testid="audit-view" @click="detail(fact.id, $event)">详情</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <footer class="paging" data-testid="audit-pagination"><span>共 {{ snapshot.total }} 条</span><button type="button" :disabled="busy || snapshot.page <= 1" @click="run(() => controller.list.setPage(snapshot.page - 1))">上一页</button><span>第 {{ snapshot.page }} 页</span><label>每页<select :value="snapshot.pageSize" :disabled="busy" @change="run(() => controller.list.setPageSize(Number(($event.target as HTMLSelectElement).value)))"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option></select></label><button type="button" :disabled="busy || snapshot.page * snapshot.pageSize >= snapshot.total" @click="run(() => controller.list.setPage(snapshot.page + 1))">下一页</button></footer>
    <section class="cleanup" aria-labelledby="cleanup-title">
      <h2 id="cleanup-title">日志清理</h2>
      <label>删除此日期前的记录<input v-model="cleanupBefore" data-testid="audit-cleanup-before" type="date" required></label>
      <button type="button" data-testid="audit-cleanup" :disabled="busy || !cleanupBefore" @click="cleanup">清理符合条件的记录</button>
    </section>
    <p v-if="cleanupStatus === 'completed'" role="status" data-testid="audit-cleanup-status">已删除 {{ controller.lastCleanup()?.deleted ?? 0 }} 条记录。<span v-if="controller.lastCleanup()?.moreEligible">仍有符合条件的记录。</span></p>
    <p v-else-if="cleanupStatus === 'refresh-failed' || cleanupStatus === 'repair-required'" role="status">清理已完成，继续操作前需要刷新列表。<button type="button" data-testid="audit-cleanup-repair" :disabled="busy" @click="repairCleanup">重试刷新</button></p>
    <div v-if="selected" class="management-dialog-backdrop" @click.self="closeDetail" @keydown.esc="closeDetail">
      <section ref="detailDialog" class="management-dialog" data-testid="audit-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="audit-detail-title" tabindex="-1">
        <header class="management-dialog__header"><h2 id="audit-detail-title">审计详情</h2><button type="button" aria-label="关闭" @click="closeDetail">×</button></header>
        <div class="management-dialog__body"><dl><dt>对象</dt><dd class="long-text">{{ selected.subject }}</dd><dt>操作者</dt><dd class="long-text">{{ selected.actorRef ?? selected.actorType }}</dd><dt>结果</dt><dd>{{ outcomeLabel(selected.outcome) }}</dd><dt>发生时间</dt><dd>{{ formatDate(selected.occurredAt) }}</dd></dl></div>
        <footer class="management-dialog__footer"><button type="button" @click="closeDetail">关闭</button></footer>
      </section>
    </div>
  </main>
</template>

<style scoped>
.audit-page { display: grid; gap: 14px; }
h1, h2 { margin: 0; letter-spacing: 0; }
.filters { display: grid; grid-template-columns: repeat(3, minmax(140px, 1fr)); gap: 12px; align-items: end; }
label { display: grid; gap: 6px; }
.commands, .paging, .cleanup { display: flex; gap: 10px; align-items: end; }
.table-wrap { overflow-x: auto; }
.table-wrap :is(th, td) { white-space: nowrap; }
.long-text { max-width: 360px; overflow-wrap: anywhere; white-space: normal; }
.cleanup { padding: 14px; background: var(--ga-bg-container); border: 1px solid var(--ga-border-light); border-radius: var(--ga-radius-lg); box-shadow: var(--ga-shadow-sm); }
.cleanup h2 { margin-right: auto; font-size: 15px; }
dl { display: grid; width: 100%; grid-template-columns: 100px 1fr; gap: 10px; margin: 0; }
@media (max-width: 760px) { .filters { grid-template-columns: 1fr 1fr; } .commands { grid-column: 1 / -1; } .cleanup { align-items: stretch; flex-direction: column; } }
@media (max-width: 520px) { .filters { grid-template-columns: 1fr; } .commands { grid-column: auto; } }
</style>
