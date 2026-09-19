<script setup lang="ts">
import { UploadIcon, FileIcon, XIcon } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'
import { filesPermissions, type FileMetadata, type FileQuery, type UploadCandidate } from '@go-admin-plus/domain-files'
import type { PlatformPort } from '@go-admin-plus/platform'
import { AppPage, EmptyState, Pagination, QueryBar, StatusTag, TableToolbar } from '@go-admin-plus/ui/components'
import type { FilesController } from './files-controller'

const props = defineProps<{ controller: FilesController; platform: PlatformPort }>()
const emit = defineEmits<{ sessionRequired: [] }>()
const revision = ref(0), search = ref(''), selectedUpload = ref<UploadCandidate | null>(null), fileInput = ref<HTMLInputElement | null>(null), localError = ref<string | null>(null), dragActive = ref(false)
const uploadRules=ref({maxUploadBytes:10*1024*1024,allowedTypes:['application/pdf','image/jpeg','image/png','text/plain'] as ReadonlyArray<string>})
const nativeBusy=ref(false)
const snapshot = computed(() => { void revision.value; return props.controller.list.snapshot() })
const failure = computed(() => { void revision.value; return props.controller.failure() })
const failureReference = computed(() => { void revision.value; return props.controller.failureTraceId() })
const projectionVisible = computed(() => { void revision.value; return props.controller.projectionVisible })
const blocked = computed(() => { void revision.value; return nativeBusy.value || props.controller.busy || props.controller.pendingRepair })
const pageBusy = computed(() => snapshot.value.loading && !projectionVisible.value)
const uploadBlocked = computed(() => blocked.value || failure.value === 'quota' || failure.value === 'capacity')
const can = (permission: typeof filesPermissions[keyof typeof filesPermissions]) => { void revision.value; return props.controller.can(permission) }
const canRead = computed(() => can(filesPermissions.read)), canWrite = computed(() => can(filesPermissions.write)), canDelete = computed(() => can(filesPermissions.delete))
const selectedRows = computed(() => snapshot.value.rows.filter(row => snapshot.value.selectedKeys.includes(row.id)))
const failureMessage = computed(() => {
  const current = failure.value
  return current ? ({ relogin: '会话已失效，请重新登录。', forbidden: '没有执行该文件操作的权限。', validation: '请检查查询或上传内容。', content: '文件大小、类型或内容不符合上传规则。', 'not-found': '文件已不存在，请刷新列表。', conflict: '文件状态已变化，请刷新后重试。', quota: '当前账号文件容量已用尽。请下载或删除文件释放空间。', capacity: '存储空间处于保护状态，暂时停止上传。下载和删除仍可使用。', unavailable: '文件服务暂不可用。' } as const)[current] : ''
})
type FileSortKey = FileQuery['sort']
const currentSort = computed(() => snapshot.value.sort ?? { key: 'createdAt', direction: 'descending' as const })
const sortDirection = (key: FileSortKey) => currentSort.value.key === key ? currentSort.value.direction : 'none'
const sortMarker = (key: FileSortKey) => currentSort.value.key === key ? currentSort.value.direction === 'ascending' ? '↑' : '↓' : ''
const formatDate = (value: string) => new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
const formatBytes = (value: number) => value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KB` : `${(value / 1024 / 1024).toFixed(1)} MB`
const settle = async (operation: () => Promise<unknown>) => { try { await operation() } catch {} finally { revision.value += 1; if (props.controller.failure() === 'relogin') emit('sessionRequired') } }
const refresh = () => settle(() => props.controller.pendingRepair ? props.controller.repairProjection() : props.controller.list.refresh())
const searchFiles = () => settle(() => props.controller.list.search({ search: search.value }))
const resetSearch = () => { search.value = ''; void settle(() => props.controller.list.reset()) }
const sortBy = (key: FileSortKey) => settle(() => props.controller.list.setSort({ key, direction: currentSort.value.key === key && currentSort.value.direction === 'ascending' ? 'descending' : 'ascending' }))
const selectFile = (file: File | undefined) => { selectedUpload.value = file ? { name: file.name, type: file.type, size: file.size, body: file } : null; localError.value = null }
const chooseFile = (event: Event) => selectFile((event.target as HTMLInputElement).files?.[0])
const openFilePicker = () => { if (!uploadBlocked.value) fileInput.value?.click() }
const handleDrop = (event: DragEvent) => { event.preventDefault(); dragActive.value = false; if (!uploadBlocked.value) selectFile(event.dataTransfer?.files?.[0]) }
const chooseHostFile = async () => {
  localError.value = null
  if (props.platform.uploadManagedFile) {
    nativeBusy.value=true
    try { if (await props.platform.uploadManagedFile()) await refresh() } catch { localError.value='上传失败，请检查会话、文件类型和服务器上传限制' } finally { nativeBusy.value=false }
    return
  }
  if (!props.platform.listCapabilities().has('file-open')) { localError.value = '当前运行环境不支持选择文件'; return }
  try { const file = await props.platform.pickFile(); selectedUpload.value = file ? { name: file.name, type: file.mediaType, size: file.bytes.length, body: new Blob([file.bytes.slice().buffer], { type: file.mediaType }) } : null }
  catch { selectedUpload.value = null; localError.value = '选择文件失败' }
}
const clearUpload = () => { selectedUpload.value = null; localError.value = null; dragActive.value = false; if (fileInput.value) fileInput.value.value = '' }
const upload = async () => {
  if (!selectedUpload.value) { localError.value = '请选择文件'; return }
  const result = await props.controller.upload(selectedUpload.value)
  if (result === 'invalid') localError.value = '不支持该文件或文件不符合上传规则'
  if (result === 'completed') { props.controller.takeCompletion(); clearUpload() }
  revision.value += 1
  if (props.controller.failure() === 'relogin') emit('sessionRequired')
}
const remove = (rows: ReadonlyArray<FileMetadata>) => settle(async () => { if (await props.controller.remove(rows) === 'completed') props.controller.takeCompletion() })
const toggle = (row: FileMetadata, checked: boolean) => { const rows = checked ? [...selectedRows.value, row] : selectedRows.value.filter(candidate => candidate.id !== row.id); props.controller.list.select(rows); revision.value += 1 }
const download = async (row: FileMetadata) => {
  if(props.platform.downloadManagedFile){try{await props.platform.downloadManagedFile(row.id,row.originalName)}catch{localError.value='下载失败，请检查会话或重新选择保存位置'};return}
  const blob = await props.controller.download(row); revision.value += 1
  if (!blob) return
  if (props.platform.runtime === 'desktop') {
    if (!props.platform.listCapabilities().has('file-save')) { localError.value = '当前运行环境不支持保存文件'; return }
    try { await props.platform.saveFile({ name: row.originalName, mediaType: row.mediaType, bytes: new Uint8Array(await blob.arrayBuffer()) }) } catch { localError.value = '保存文件失败' }
    return
  }
  const url = URL.createObjectURL(blob)
  try { const anchor = document.createElement('a'); anchor.href = url; anchor.download = row.originalName; anchor.click() } finally { URL.revokeObjectURL(url) }
}
onMounted(() => { void props.controller.uploadPolicy().then(value=>{uploadRules.value=value}).catch(()=>{localError.value='读取上传限制失败'}); void settle(() => props.controller.list.refresh()) })
</script>

<template>
  <AppPage title="文件管理" description="上传、下载和释放当前账号的文件空间" :busy="pageBusy">
    <template #actions><StatusTag :tone="failure === 'quota' || failure === 'capacity' ? 'warning' : 'info'" :label="`${snapshot.total} 个文件`" /></template>
    <p v-if="failure" class="page-alert" role="alert" :data-failure="failure">{{ failureMessage }}<span v-if="failureReference"> 参考编号：{{ failureReference }}</span></p>
    <template v-if="projectionVisible && canRead">
      <QueryBar :busy="blocked" :reset-disabled="!search" @search="searchFiles" @reset="resetSearch"><label>文件名称<input v-model="search" name="search" maxlength="100" placeholder="请输入文件名称"></label></QueryBar>
      <section v-if="canWrite" class="upload-panel" data-testid="files-upload" :aria-disabled="uploadBlocked">
        <div class="upload-heading"><div class="upload-icon"><UploadIcon :size="20" /></div><div><strong>上传文件</strong><p>支持 {{ uploadRules.allowedTypes.join('、') }}，单个文件不超过 {{ formatBytes(uploadRules.maxUploadBytes) }}</p></div></div>
        <div v-if="platform.runtime === 'web'" class="upload-dropzone" :class="{ 'is-dragging': dragActive, 'has-file': selectedUpload }" role="button" tabindex="0" :aria-disabled="uploadBlocked" @click="openFilePicker" @keydown.enter.prevent="openFilePicker" @keydown.space.prevent="openFilePicker" @dragover.prevent="dragActive = true" @dragleave.prevent="dragActive = false" @drop="handleDrop"><input ref="fileInput" name="file" type="file" :accept="uploadRules.allowedTypes.join(',')" :disabled="uploadBlocked" @change="chooseFile"><template v-if="selectedUpload"><FileIcon :size="18" /><span class="selected-file">{{ selectedUpload.name }}</span><button type="button" class="clear-file" aria-label="移除已选文件" title="移除已选文件" @click.stop="clearUpload"><XIcon :size="15" /></button></template><template v-else><UploadIcon :size="22" /><span>拖拽文件到这里，或 <b>选择文件</b></span></template></div>
        <div v-else class="upload-dropzone host-dropzone" :class="{ 'has-file': selectedUpload }" role="button" tabindex="0" :aria-disabled="uploadBlocked" @click="chooseHostFile" @keydown.enter.prevent="chooseHostFile"><template v-if="selectedUpload"><FileIcon :size="18" /><span class="selected-file">{{ selectedUpload.name }}</span><button type="button" class="clear-file" aria-label="移除已选文件" title="移除已选文件" @click.stop="clearUpload"><XIcon :size="15" /></button></template><template v-else><UploadIcon :size="22" /><span>选择并上传文件</span></template></div>
        <div class="upload-actions"><button type="button" class="upload-submit" :disabled="uploadBlocked || !selectedUpload" @click="upload"><UploadIcon :size="16" />开始上传</button><p v-if="localError" role="alert">{{ localError }}</p></div>
      </section>
      <TableToolbar :selected-count="selectedRows.length" :busy="blocked" @refresh="refresh"><button v-if="canDelete" type="button" data-testid="files-delete-selected" :disabled="blocked || selectedRows.length === 0" @click="remove(selectedRows)">批量删除</button></TableToolbar>
      <div class="table-scroll" role="region" aria-label="文件列表">
        <table v-if="snapshot.rows.length > 0"><thead><tr><th v-if="canDelete" scope="col">选择</th><th scope="col" :aria-sort="sortDirection('name')"><button type="button" :disabled="blocked" @click="sortBy('name')">文件名称 {{ sortMarker('name') }}</button></th><th scope="col">类型</th><th scope="col" :aria-sort="sortDirection('sizeBytes')"><button type="button" :disabled="blocked" @click="sortBy('sizeBytes')">大小 {{ sortMarker('sizeBytes') }}</button></th><th scope="col" :aria-sort="sortDirection('createdAt')"><button type="button" :disabled="blocked" @click="sortBy('createdAt')">创建时间 {{ sortMarker('createdAt') }}</button></th><th scope="col">操作</th></tr></thead><tbody><tr v-for="row in snapshot.rows" :key="row.id" :data-file-id="row.id"><td v-if="canDelete"><input type="checkbox" :checked="snapshot.selectedKeys.includes(row.id)" :aria-label="`选择 ${row.originalName}`" :disabled="blocked" @change="toggle(row, ($event.target as HTMLInputElement).checked)"></td><td>{{ row.originalName }}</td><td>{{ row.mediaType }}</td><td>{{ formatBytes(row.sizeBytes) }}</td><td>{{ formatDate(row.createdAt) }}</td><td class="row-actions"><button type="button" :disabled="blocked" @click="download(row)">下载</button><button v-if="canDelete" type="button" :disabled="blocked" @click="remove([row])">删除</button></td></tr></tbody></table>
        <EmptyState v-else title="暂无文件" />
      </div>
      <Pagination :page="snapshot.page" :page-size="snapshot.pageSize" :total="snapshot.total" :disabled="blocked" @update:page="settle(() => controller.list.setPage($event))" @update:page-size="settle(() => controller.list.setPageSize($event))" />
    </template>
    <EmptyState v-else-if="!pageBusy" :title="failureMessage || '暂无可用文件数据'" action-label="重试" @action="refresh" />
  </AppPage>
</template>

<style scoped>
.page-alert{margin:0}.upload-panel{display:grid;gap:12px;padding:16px;background:var(--ga-bg-container);border:1px solid var(--ga-border-light);border-radius:var(--ga-radius-lg);box-shadow:var(--ga-shadow-sm)}.upload-heading{display:flex;align-items:center;gap:10px}.upload-heading strong{font-size:14px}.upload-heading p{margin:3px 0 0;color:var(--ga-text-3);font-size:12px}.upload-icon{display:grid;width:36px;height:36px;place-items:center;color:var(--ga-brand);background:var(--ga-brand-soft);border-radius:10px}.upload-dropzone{position:relative;display:flex;min-height:76px;align-items:center;justify-content:center;gap:8px;padding:12px;color:var(--ga-text-3);background:var(--ga-bg-subtle);border:1px dashed var(--ga-border);border-radius:var(--ga-radius);cursor:pointer;transition:border-color .15s,background .15s}.upload-dropzone:hover,.upload-dropzone.is-dragging{color:var(--ga-brand);background:var(--ga-brand-soft);border-color:var(--ga-brand)}.upload-dropzone.has-file{justify-content:flex-start;color:var(--ga-text-1);border-style:solid}.upload-dropzone input{position:absolute;width:1px;height:1px;overflow:hidden;opacity:0;pointer-events:none}.upload-dropzone b{color:var(--ga-brand);font-weight:600}.selected-file{max-width:calc(100% - 48px);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--ga-text-1);font-size:13px}.clear-file{display:grid;width:26px;height:26px;margin-left:auto;place-items:center;color:var(--ga-text-3);background:transparent;border:0;border-radius:var(--ga-radius);cursor:pointer}.clear-file:hover{color:var(--ga-danger);background:var(--ga-danger-soft)}.upload-actions{display:flex;align-items:center;gap:10px}.upload-actions p{margin:0;color:var(--ga-danger);font-size:12px}.upload-submit{display:inline-flex;min-height:34px;align-items:center;gap:7px;padding:0 14px;color:#fff;background:var(--ga-brand);border:1px solid var(--ga-brand);border-radius:var(--ga-radius);cursor:pointer}.upload-submit:disabled{cursor:not-allowed;opacity:.5}.table-scroll{min-width:0;overflow:auto}.row-actions{display:flex;gap:8px;white-space:nowrap}@media(max-width:760px){.upload-actions{align-items:stretch;flex-direction:column}.upload-submit{justify-content:center}}
</style>
