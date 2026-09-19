<script setup lang="ts">
import { createDesktopSession } from '@go-admin-plus/adapter-desktop'
import { computed, onMounted, ref } from 'vue'

import { createFirstSetupClient, type FirstSetupInput } from './client'

type GateState = 'loading' | 'required' | 'recovery' | 'workspace' | 'unavailable'

const client = createFirstSetupClient()
const session = createDesktopSession()
const state = ref<GateState>('loading')
const username = ref('admin')
const displayName = ref('系统管理员')
const email = ref('')
const password = ref('')
const confirmation = ref('')
const error = ref('')
const submitting = ref(false)
const workspaceKey = ref(0)

const passwordsMatch = computed(() => password.value === confirmation.value)
const passwordTooCommon = computed(() => new Set([
  '1234567890', '123456789', 'password123', 'administrator', 'administrator password', 'admin123456'
]).has(password.value.trim().toLowerCase()))

const load = async () => {
  state.value = 'loading'
  error.value = ''
  try {
    const current = await client.state()
    state.value = current === 'required' ? 'required' : current === 'login-required' ? 'workspace' : 'unavailable'
  } catch {
    state.value = 'unavailable'
  }
}

const openWorkspace = () => {
  workspaceKey.value += 1
  state.value = 'workspace'
}

const continueToLogin = async () => {
  error.value = ''
  submitting.value = true
  try {
    await session.logout()
    openWorkspace()
  } catch {
    error.value = '无法进入登录，请重试'
  } finally {
    submitting.value = false
  }
}

const submit = async () => {
  error.value = ''
  if (!passwordsMatch.value) {
    error.value = '两次输入的密码不一致'
    return
  }
  if (password.value.length < 10 || passwordTooCommon.value) {
    error.value = '请设置长度至少为 10 位且不易猜测的密码'
    return
  }
  const input: FirstSetupInput = {
    username: username.value.trim(),
    displayName: displayName.value.trim(),
    email: email.value.trim(),
    password: password.value
  }
  submitting.value = true
  try {
    const outcome = await client.submit(input)
    if (outcome.state === 'complete') openWorkspace()
    else state.value = 'recovery'
  } catch {
    try {
      state.value = await client.state() === 'login-required' ? 'recovery' : 'required'
    } catch {
      state.value = 'unavailable'
    }
    if (state.value === 'required') error.value = '无法完成首次设置，请检查输入后重试'
  } finally {
    input.password = ''
    password.value = ''
    confirmation.value = ''
    submitting.value = false
  }
}

onMounted(() => { void load() })
</script>

<template>
  <slot v-if="state === 'workspace'" :workspace-key="workspaceKey" />

  <main v-else class="first-setup-shell">
    <section v-if="state === 'loading'" class="first-setup-state" aria-live="polite">
      <span class="first-setup-spinner" aria-hidden="true" />
      <p>正在连接服务</p>
    </section>

    <section v-else-if="state === 'required'" class="first-setup-panel" aria-labelledby="first-setup-title">
      <header>
        <span class="first-setup-mark" aria-hidden="true">G</span>
        <div>
          <p class="first-setup-product">Go Admin Plus Desktop</p>
          <h1 id="first-setup-title">创建首位管理员</h1>
        </div>
      </header>

      <form @submit.prevent="submit">
        <label>
          <span>用户名</span>
          <input v-model="username" name="username" autocomplete="username" minlength="3" maxlength="64" required :disabled="submitting">
        </label>
        <label>
          <span>显示名称</span>
          <input v-model="displayName" name="displayName" autocomplete="name" maxlength="80" required :disabled="submitting">
        </label>
        <label class="first-setup-wide">
          <span>邮箱</span>
          <input v-model="email" name="email" type="email" autocomplete="email" maxlength="254" required :disabled="submitting">
        </label>
        <label>
          <span>密码</span>
          <input v-model="password" name="password" type="password" autocomplete="new-password" minlength="10" maxlength="128" required :disabled="submitting">
        </label>
        <label>
          <span>确认密码</span>
          <input v-model="confirmation" name="confirmation" type="password" autocomplete="new-password" minlength="10" maxlength="128" required :disabled="submitting">
        </label>
        <p v-if="error" class="first-setup-error first-setup-wide" role="alert">{{ error }}</p>
        <div class="first-setup-actions first-setup-wide">
          <button type="submit" :disabled="submitting">{{ submitting ? '正在创建' : '创建并进入工作区' }}</button>
        </div>
      </form>
    </section>

    <section v-else-if="state === 'recovery'" class="first-setup-panel first-setup-recovery" aria-labelledby="first-setup-recovery-title">
      <span class="first-setup-status" aria-hidden="true">!</span>
      <h1 id="first-setup-recovery-title">管理员已创建</h1>
      <p>首次会话未能完成，请使用刚才创建的账号登录。</p>
      <p v-if="error" class="first-setup-error" role="alert">{{ error }}</p>
      <button type="button" :disabled="submitting" @click="continueToLogin">{{ submitting ? '正在进入' : '进入登录' }}</button>
    </section>

    <section v-else class="first-setup-panel first-setup-recovery" aria-labelledby="first-setup-unavailable-title">
      <span class="first-setup-status is-muted" aria-hidden="true">!</span>
      <h1 id="first-setup-unavailable-title">后端服务暂不可用</h1>
      <p>首次设置状态无法确认。</p>
      <button type="button" @click="load">重试</button>
    </section>
  </main>
</template>

<style scoped>
.first-setup-shell { min-height: 100vh; display: grid; place-items: center; padding: 32px; background: var(--ga-bg-body); }
.first-setup-panel { width: min(680px, 100%); padding: 32px; background: var(--ga-bg-container); border: 1px solid var(--ga-border-light); border-top: 4px solid var(--ga-brand); border-radius: var(--ga-radius-lg); box-shadow: var(--ga-shadow); }
.first-setup-panel header { display: flex; align-items: center; gap: 14px; margin-bottom: 28px; }
.first-setup-mark { display: grid; width: 42px; height: 42px; flex: 0 0 42px; place-items: center; color: #fff; background: var(--ga-brand); border-radius: var(--ga-radius); font-size: 20px; font-weight: 700; }
.first-setup-product { margin: 0 0 3px; color: var(--ga-text-3); font-size: 12px; font-weight: 650; text-transform: uppercase; }
.first-setup-panel h1 { margin: 0; font-size: 22px; letter-spacing: 0; }
.first-setup-panel form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.first-setup-panel label { display: grid; min-width: 0; gap: 6px; color: var(--ga-text-2); font-size: 13px; font-weight: 600; }
.first-setup-panel input { width: 100%; height: var(--ga-control-height); padding: 0 11px; color: var(--ga-text-1); background: var(--ga-bg-container); border: 1px solid var(--ga-border); border-radius: var(--ga-radius); outline: none; }
.first-setup-panel input:focus { border-color: var(--ga-brand); box-shadow: 0 0 0 2px color-mix(in srgb, var(--ga-brand) 18%, transparent); }
.first-setup-panel input:disabled { color: var(--ga-text-disabled); background: var(--ga-bg-subtle); }
.first-setup-wide { grid-column: 1 / -1; }
.first-setup-actions { display: flex; justify-content: flex-end; padding-top: 6px; }
.first-setup-panel button { min-height: var(--ga-control-height); padding: 0 18px; color: #fff; background: var(--ga-brand); border: 1px solid var(--ga-brand); border-radius: var(--ga-radius); cursor: pointer; font-weight: 650; }
.first-setup-panel button:disabled { cursor: wait; opacity: .65; }
.first-setup-error { margin: 0; padding: 10px 12px; color: var(--ga-danger); background: color-mix(in srgb, var(--ga-danger) 8%, var(--ga-bg-container)); border-left: 3px solid var(--ga-danger); font-size: 13px; }
.first-setup-state { display: grid; place-items: center; gap: 12px; color: var(--ga-text-2); }
.first-setup-state p { margin: 0; }
.first-setup-spinner { width: 28px; height: 28px; border: 3px solid var(--ga-border); border-top-color: var(--ga-brand); border-radius: 50%; animation: first-setup-spin .8s linear infinite; }
.first-setup-recovery { max-width: 480px; text-align: center; border-top-color: var(--ga-warning); }
.first-setup-recovery p { margin: 10px 0 24px; color: var(--ga-text-2); }
.first-setup-status { display: grid; width: 40px; height: 40px; margin: 0 auto 14px; place-items: center; color: #fff; background: var(--ga-warning); border-radius: 50%; font-weight: 800; }
.first-setup-status.is-muted { background: var(--ga-info); }
@keyframes first-setup-spin { to { transform: rotate(360deg); } }
@media (max-width: 620px) {
  .first-setup-shell { place-items: start stretch; padding: 16px; }
  .first-setup-panel { padding: 22px 18px; }
  .first-setup-panel form { grid-template-columns: 1fr; }
  .first-setup-wide { grid-column: auto; }
  .first-setup-actions button { width: 100%; }
}
@media (prefers-reduced-motion: reduce) { .first-setup-spinner { animation: none; } }
</style>
