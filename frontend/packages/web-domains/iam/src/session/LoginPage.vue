<script setup lang="ts">
import {
  EyeIcon,
  EyeOffIcon,
  LoaderCircleIcon,
  LockKeyholeIcon,
  LogInIcon,
  ShieldCheckIcon,
  UserRoundIcon
} from '@lucide/vue'
import { nextTick, reactive, ref } from 'vue'
import type { SessionController } from '@go-admin-plus/domain-iam/session'

const props = defineProps<{ controller: SessionController }>()
const emit = defineEmits<{ authenticated: [] }>()
const credentials = reactive({ username: '', password: '' })
const submitting = ref(false)
const failed = ref(false)
const passwordVisible = ref(false)
const passwordInput = ref<HTMLInputElement | null>(null)

const togglePassword = async () => {
  passwordVisible.value = !passwordVisible.value
  await nextTick()
  passwordInput.value?.focus()
}

const submit = async () => {
  if (submitting.value) return
  submitting.value = true
  failed.value = false
  try { await props.controller.login(credentials) } finally {
    credentials.password = ''
    passwordVisible.value = false
    submitting.value = false
  }
  if (props.controller.state().status === 'authenticated') emit('authenticated')
  else failed.value = true
}
</script>

<template>
  <main class="login-page">
    <section class="login-page__visual" aria-label="Go Admin Plus">
      <header class="login-page__brand">
        <span><ShieldCheckIcon :size="22" aria-hidden="true" /></span>
        <strong>Go Admin Plus</strong>
      </header>

      <div class="login-page__preview" aria-hidden="true">
        <aside class="login-page__preview-nav">
          <span class="login-page__preview-logo" />
          <span v-for="index in 7" :key="index" :class="{ active: index === 2 }" />
        </aside>
        <div class="login-page__preview-main">
          <div class="login-page__preview-top"><i /><i /><i /></div>
          <div class="login-page__preview-heading"><strong /><span /></div>
          <div class="login-page__preview-filter"><span /><span /><i /></div>
          <div class="login-page__preview-table">
            <div class="login-page__preview-row login-page__preview-row--head"><span /><span /><span /><span /></div>
            <div v-for="index in 5" :key="index" class="login-page__preview-row"><span /><span /><span /><i /></div>
          </div>
        </div>
      </div>
      <p class="login-page__visual-title">管理控制台</p>
    </section>

    <section class="login-page__panel">
      <form class="login-page__form" aria-label="登录" @submit.prevent="submit">
        <div class="login-page__form-brand">
          <span><ShieldCheckIcon :size="20" aria-hidden="true" /></span>
          <strong>Go Admin Plus</strong>
        </div>
        <div class="login-page__heading">
          <h1>欢迎回来</h1>
          <p>登录管理控制台</p>
        </div>

        <label>
          <span>账号</span>
          <span class="login-page__field">
            <UserRoundIcon :size="17" aria-hidden="true" />
            <input v-model.trim="credentials.username" aria-label="账号" autocomplete="username" placeholder="请输入账号" autofocus required minlength="3" maxlength="64">
          </span>
        </label>
        <label>
          <span>密码</span>
          <span class="login-page__field">
            <LockKeyholeIcon :size="17" aria-hidden="true" />
            <input ref="passwordInput" v-model="credentials.password" aria-label="密码" autocomplete="current-password" :type="passwordVisible ? 'text' : 'password'" placeholder="请输入密码" required minlength="10" maxlength="128">
            <button
              class="login-page__password-toggle"
              type="button"
              :aria-label="passwordVisible ? '隐藏密码' : '显示密码'"
              :aria-pressed="passwordVisible"
              :title="passwordVisible ? '隐藏密码' : '显示密码'"
              @click="togglePassword"
            ><EyeOffIcon v-if="passwordVisible" :size="17" aria-hidden="true" /><EyeIcon v-else :size="17" aria-hidden="true" /></button>
          </span>
        </label>

        <p v-if="failed" class="login-page__error" role="alert">账号或密码无法验证，请重试。</p>
        <button class="login-page__submit" type="submit" :disabled="submitting">
          <LoaderCircleIcon v-if="submitting" class="login-page__loading" :size="17" aria-hidden="true" />
          <LogInIcon v-else :size="17" aria-hidden="true" />
          {{ submitting ? '登录中' : '登录' }}
        </button>
        <p class="login-page__tip">忘记密码请联系系统管理员重置</p>
      </form>
    </section>
  </main>
</template>

<style scoped>
.login-page { display: grid; width: 100vw; min-height: 100vh; grid-template-columns: minmax(0, 1.25fr) minmax(420px, .75fr); overflow: hidden; background: var(--ga-bg-body); }
.login-page__visual { position: relative; display: grid; min-width: 0; align-content: space-between; padding: 38px clamp(36px, 6vw, 88px) 42px; overflow: hidden; color: #fff; background: #171c25; }
.login-page__visual::before, .login-page__visual::after { position: absolute; content: ''; pointer-events: none; }
.login-page__visual::before { inset: 80px 8% 110px; border: 1px solid rgb(255 255 255 / 4%); border-radius: var(--ga-radius-lg); transform: rotate(-4deg); }
.login-page__visual::after { right: 7%; bottom: 12%; width: 120px; height: 120px; border-right: 1px solid rgb(107 156 255 / 22%); border-bottom: 1px solid rgb(107 156 255 / 22%); }
.login-page__brand, .login-page__form-brand { display: flex; position: relative; z-index: 1; align-items: center; gap: 10px; }
.login-page__brand > span, .login-page__form-brand > span { display: grid; width: 36px; height: 36px; place-items: center; color: #fff; background: #2563eb; border-radius: var(--ga-radius); }
.login-page__brand strong, .login-page__form-brand strong { font-size: 15px; font-weight: 700; }
.login-page__preview { position: relative; z-index: 1; display: grid; width: min(100%, 720px); aspect-ratio: 16 / 9; grid-template-columns: 104px minmax(0, 1fr); justify-self: center; overflow: hidden; background: #f7f9fc; border: 1px solid rgb(255 255 255 / 16%); border-radius: var(--ga-radius-lg); box-shadow: 0 30px 70px rgb(0 0 0 / 34%); transform: perspective(1200px) rotateY(-3deg) rotateX(1deg); }
.login-page__preview-nav { display: grid; grid-auto-rows: 28px; align-content: start; gap: 6px; padding: 15px 10px; background: #111720; }
.login-page__preview-nav > span:not(.login-page__preview-logo) { width: 100%; border-radius: 4px; background: #222b38; }
.login-page__preview-nav > span.active { background: #2563eb; }
.login-page__preview-logo { width: 28px; height: 28px; margin: 0 0 12px 5px; background: #fff; border-radius: 5px; }
.login-page__preview-main { display: grid; min-width: 0; grid-template-rows: 42px 50px 54px minmax(0, 1fr); padding: 0 14px 14px; color: #1f2937; }
.login-page__preview-top { display: flex; align-items: center; justify-content: flex-end; gap: 6px; margin: 0 -14px; padding: 0 14px; background: #fff; border-bottom: 1px solid #e7ebf0; }
.login-page__preview-top i { width: 18px; height: 18px; border-radius: 50%; background: #e8edf5; }
.login-page__preview-heading { display: grid; align-content: center; gap: 6px; }
.login-page__preview-heading strong { width: 105px; height: 9px; background: #303947; border-radius: 3px; }
.login-page__preview-heading span { width: 175px; height: 6px; background: #c9d0da; border-radius: 3px; }
.login-page__preview-filter { display: flex; align-items: center; gap: 8px; padding: 9px; background: #fff; border: 1px solid #e7ebf0; border-radius: 6px; }
.login-page__preview-filter span { width: 120px; height: 24px; border: 1px solid #d7dde6; border-radius: 4px; }
.login-page__preview-filter i { width: 54px; height: 24px; margin-left: auto; background: #2563eb; border-radius: 4px; }
.login-page__preview-table { align-self: stretch; margin-top: 10px; overflow: hidden; background: #fff; border: 1px solid #e7ebf0; border-radius: 6px; }
.login-page__preview-row { display: grid; height: 16.666%; min-height: 22px; grid-template-columns: 1.2fr 1fr 1fr 52px; align-items: center; gap: 14px; padding: 0 11px; border-bottom: 1px solid #eef1f5; }
.login-page__preview-row:last-child { border-bottom: 0; }
.login-page__preview-row span { height: 5px; background: #d5dbe4; border-radius: 2px; }
.login-page__preview-row i { width: 32px; height: 12px; justify-self: end; background: #e8f0ff; border-radius: 4px; }
.login-page__preview-row--head { background: #f8fafc; }
.login-page__preview-row--head span { background: #aeb8c5; }
.login-page__visual-title { position: relative; z-index: 1; margin: 0; color: #8f9aaa; font-size: 12px; font-weight: 600; }
.login-page__panel { display: grid; place-items: center; padding: 36px; background: var(--ga-bg-container); }
.login-page__form { display: grid; width: min(100%, 360px); gap: 18px; }
.login-page__form-brand { display: none; color: var(--ga-text-1); }
.login-page__heading { margin-bottom: 8px; }
.login-page__heading h1 { margin: 0; color: var(--ga-text-1); font-size: 28px; font-weight: 700; line-height: 1.3; letter-spacing: 0; }
.login-page__heading p { margin: 7px 0 0; color: var(--ga-text-3); font-size: 13px; }
.login-page__form label { display: grid; gap: 8px; color: var(--ga-text-2); font-size: 13px; font-weight: 600; }
.login-page__field { position: relative; display: block; color: var(--ga-text-3); }
.login-page__field > svg { position: absolute; top: 50%; left: 13px; z-index: 1; transform: translateY(-50%); pointer-events: none; }
.login-page__field input { width: 100%; min-height: 44px; padding: 0 42px; color: var(--ga-text-1); background: var(--ga-bg-container); border: 1px solid var(--ga-border); border-radius: var(--ga-radius-lg); outline: none; transition: border-color .16s ease, box-shadow .16s ease; }
.login-page__field input:hover { border-color: color-mix(in srgb, var(--ga-brand), var(--ga-border) 58%); }
.login-page__field input:focus { border-color: var(--ga-brand); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ga-brand), transparent 84%); }
.login-page__password-toggle { position: absolute; top: 6px; right: 6px; display: grid; width: 32px; height: 32px; place-items: center; padding: 0; color: var(--ga-text-3); background: transparent; border: 0; border-radius: var(--ga-radius); cursor: pointer; }
.login-page__password-toggle:hover { color: var(--ga-brand); background: var(--ga-brand-soft); }
.login-page__error { margin: -4px 0 0; padding: 10px 12px; color: var(--ga-danger); background: var(--ga-danger-soft); border: 1px solid color-mix(in srgb, var(--ga-danger), transparent 72%); border-radius: var(--ga-radius); font-size: 12px; }
.login-page__submit { display: flex; min-height: 44px; align-items: center; justify-content: center; gap: 8px; margin-top: 2px; color: #fff; background: var(--ga-brand); border: 1px solid var(--ga-brand); border-radius: var(--ga-radius-lg); box-shadow: 0 8px 18px color-mix(in srgb, var(--ga-brand), transparent 74%); cursor: pointer; font-weight: 650; }
.login-page__submit:hover:not(:disabled) { background: var(--ga-brand-strong); border-color: var(--ga-brand-strong); }
.login-page__submit:disabled { cursor: not-allowed; opacity: .62; }
.login-page__loading { animation: spin .8s linear infinite; }
.login-page__tip { margin: -4px 0 0; color: var(--ga-text-3); font-size: 12px; text-align: center; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 980px) {
  .login-page { grid-template-columns: minmax(0, .9fr) minmax(400px, 1.1fr); }
  .login-page__visual { padding-inline: 30px; }
  .login-page__preview { grid-template-columns: 78px minmax(0, 1fr); }
}
@media (max-width: 760px) {
  .login-page { display: grid; grid-template-columns: 1fr; overflow: auto; }
  .login-page__visual { display: none; }
  .login-page__panel { min-height: 100vh; padding: 28px 22px; }
  .login-page__form-brand { display: flex; margin-bottom: 20px; }
}
@media (prefers-reduced-motion: reduce) { .login-page__loading { animation: none; } }
</style>
