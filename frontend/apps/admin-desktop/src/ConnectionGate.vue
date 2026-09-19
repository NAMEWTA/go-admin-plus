<script setup lang="ts">
import { createDesktopConnection, type DesktopConnectionSettings } from '@go-admin-plus/adapter-desktop'
import { nextTick, onMounted, ref } from 'vue'

const connection = createDesktopConnection()
const configured = ref(false)
const loaded = ref(false)
const busy = ref(false)
const message = ref('')
const dialog = ref<HTMLDialogElement>()
const mode = ref<'local' | 'remote'>('local')
const serverUrl = ref('')
const caCertificate = ref('')
const settings = (): DesktopConnectionSettings => ({ mode: mode.value, serverUrl: serverUrl.value, caCertificate: caCertificate.value })

const open = async () => { message.value = ''; await nextTick(); dialog.value?.showModal() }
const testConnection = async () => {
  busy.value = true
  message.value = ''
  try { await connection.test(settings()); message.value = '连接成功，服务版本兼容' }
  catch { message.value = '连接失败，请检查服务器地址、网络和 TLS 证书' }
  finally { busy.value = false }
}
const save = async () => {
  busy.value = true
  message.value = ''
  try { await connection.save(settings()) }
  catch { message.value = '保存失败，请检查设置后重试'; busy.value = false }
}
onMounted(async () => {
  try {
    const value = await connection.read()
    configured.value = value.configured
    mode.value = value.settings.mode
    serverUrl.value = value.settings.serverUrl
    caCertificate.value = value.settings.caCertificate
  } catch { message.value = '连接设置读取失败，请重新配置' }
  loaded.value = true
  if (!configured.value) await open()
})
</script>

<template>
  <div class="connection-actions">
    <button type="button" :disabled="busy" @click="open">连接设置</button>
  </div>
  <slot v-if="loaded && configured" />
  <main v-else class="connection-welcome">请选择本地使用或连接远程服务。</main>
  <dialog ref="dialog" aria-labelledby="connection-title" @cancel="!configured && $event.preventDefault()">
    <form @submit.prevent="save">
      <h2 id="connection-title">连接设置</h2>
      <label>运行模式
        <select v-model="mode" :disabled="busy">
          <option value="local">本地模式</option>
          <option value="remote">远程服务</option>
        </select>
      </label>
      <p v-if="mode === 'local'">数据保存在本机。定时任务在应用运行期间执行。</p>
      <template v-else>
        <label>服务器地址 <input v-model="serverUrl" type="url" placeholder="https://admin.example.com" required :disabled="busy"></label>
        <label>可信 CA 文件路径（可选）<input v-model="caCertificate" :disabled="busy"></label>
        <button type="button" :disabled="busy || !serverUrl" @click="testConnection">测试连接</button>
      </template>
      <p>切换后重新登录，本地与远程的数据分别保存。</p>
      <p role="status" aria-live="polite">{{ message }}</p>
      <footer>
        <button v-if="configured" type="button" :disabled="busy" @click="dialog?.close()">取消</button>
        <button type="submit" :disabled="busy">{{ busy ? '处理中…' : '保存并重新启动' }}</button>
      </footer>
    </form>
  </dialog>
</template>

<style scoped>
.connection-actions { position: fixed; right: 1rem; bottom: 1rem; z-index: 100; }
.connection-welcome { display: grid; min-height: 100vh; place-items: center; }
dialog { width: min(32rem, calc(100vw - 3rem)); padding: 1.5rem; border: 1px solid var(--el-border-color, #ddd); border-radius: .75rem; color: var(--el-text-color-primary, #222); background: var(--el-bg-color, #fff); }
dialog::backdrop { background: rgb(0 0 0 / 40%); }
form, label { display: grid; gap: .75rem; }
input, select, button { padding: .6rem; font: inherit; }
footer { display: flex; justify-content: flex-end; gap: .75rem; }
</style>
