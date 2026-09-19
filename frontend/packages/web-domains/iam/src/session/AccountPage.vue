<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import type { AccountProfile, SessionController, UpdateProfile } from '@go-admin-plus/domain-iam/session'
import { passwordsMatch } from './account-form'

const props = defineProps<{ controller: SessionController; profile: AccountProfile }>()
const emit = defineEmits<{ signedOut: [] }>()
const form = reactive({ displayName: props.profile.displayName, email: props.profile.email, avatarRef: props.profile.avatarRef })
const password = reactive({ currentPassword: '', newPassword: '', confirmPassword: '' })
const busy = ref(false)
const activeTab = ref<'profile' | 'password'>('profile')
const passwordMismatch = ref(false)
const profileTab = ref<HTMLButtonElement | null>(null)
const passwordTab = ref<HTMLButtonElement | null>(null)
const profileMark = computed(() => (props.profile.displayName.trim().charAt(0) || props.profile.username.charAt(0) || 'A').toUpperCase())

const activateTab = async (tab: 'profile' | 'password') => {
  activeTab.value = tab
  await nextTick()
  const target = tab === 'profile' ? profileTab : passwordTab
  target.value?.focus()
}

watch(() => props.profile, profile => {
  form.displayName = profile.displayName
  form.email = profile.email
  form.avatarRef = profile.avatarRef
})

const save = async () => {
  if (busy.value) return
  const avatarRef = form.avatarRef?.trim()
  const update: UpdateProfile = {
    displayName: form.displayName.trim(),
    email: form.email.trim(),
    ...(avatarRef ? { avatarRef } : {})
  }
  busy.value = true
  try { await props.controller.updateProfile(update) } finally { busy.value = false }
}
const changePassword = async () => {
  if (busy.value) return
  passwordMismatch.value = !passwordsMatch(password.newPassword, password.confirmPassword)
  if (passwordMismatch.value) return
  busy.value = true
  try {
    await props.controller.changePassword(password.currentPassword, password.newPassword)
    if (props.controller.state().status === 'unauthenticated') emit('signedOut')
  } finally {
    password.currentPassword = ''
    password.newPassword = ''
    password.confirmPassword = ''
    busy.value = false
  }
}
const logout = async () => {
  if (busy.value) return
  busy.value = true
  try { await props.controller.logout() } finally {
    busy.value = false
    if (props.controller.state().status === 'unauthenticated') emit('signedOut')
  }
}
</script>

<template>
  <main class="account-page">
    <header><h1>个人中心</h1></header>
    <p v-if="controller.state().status === 'error'" role="alert">请求未能完成，请稍后重试。</p>
    <div class="account-page__workspace">
      <aside class="account-page__summary" aria-labelledby="account-summary-title">
        <h2 id="account-summary-title">个人信息</h2>
        <div class="account-page__identity">
          <span class="account-page__avatar" aria-hidden="true">{{ profileMark }}</span>
          <strong>{{ profile.displayName }}</strong>
          <span>@{{ profile.username }}</span>
        </div>
        <dl>
          <div><dt>用户名称</dt><dd>{{ profile.username }}</dd></div>
          <div><dt>用户昵称</dt><dd>{{ profile.displayName }}</dd></div>
          <div><dt>用户邮箱</dt><dd>{{ profile.email }}</dd></div>
          <div><dt>头像元数据</dt><dd>{{ profile.avatarRef ? '已设置' : '未设置' }}</dd></div>
        </dl>
        <button class="account-page__logout" type="button" :disabled="busy" @click="logout">退出登录</button>
      </aside>

      <section class="account-page__detail" aria-labelledby="account-detail-title">
        <h2 id="account-detail-title">基本资料</h2>
        <nav class="tabs" role="tablist" aria-label="账户设置">
          <button
            ref="profileTab"
            id="account-profile-tab"
            type="button"
            role="tab"
            :aria-selected="activeTab === 'profile'"
            aria-controls="account-profile-panel"
            :tabindex="activeTab === 'profile' ? 0 : -1"
            @click="activeTab = 'profile'"
            @keydown.right.prevent="activateTab('password')"
            @keydown.end.prevent="activateTab('password')"
          >基本资料</button>
          <button
            ref="passwordTab"
            id="account-password-tab"
            type="button"
            role="tab"
            :aria-selected="activeTab === 'password'"
            aria-controls="account-password-panel"
            :tabindex="activeTab === 'password' ? 0 : -1"
            @click="activeTab = 'password'"
            @keydown.left.prevent="activateTab('profile')"
            @keydown.home.prevent="activateTab('profile')"
          >修改密码</button>
        </nav>

        <form
          v-if="activeTab === 'profile'"
          id="account-profile-panel"
          role="tabpanel"
          aria-labelledby="account-profile-tab"
          @submit.prevent="save"
        >
          <label>用户昵称<input v-model.trim="form.displayName" required maxlength="80"></label>
          <label>邮箱<input v-model.trim="form.email" type="email" autocomplete="email" required maxlength="254"></label>
          <label>头像引用<input v-model.trim="form.avatarRef" autocomplete="off" maxlength="261" pattern="files/[A-Za-z0-9][A-Za-z0-9._/-]{0,254}"></label>
          <div class="account-page__actions"><button type="submit" :disabled="busy">保存资料</button></div>
        </form>

        <form
          v-else
          id="account-password-panel"
          role="tabpanel"
          aria-labelledby="account-password-tab"
          @submit.prevent="changePassword"
        >
          <label>当前密码<input v-model="password.currentPassword" type="password" autocomplete="current-password" required minlength="10" maxlength="128"></label>
          <label>新密码<input v-model="password.newPassword" type="password" autocomplete="new-password" required minlength="10" maxlength="128" @input="passwordMismatch = false"></label>
          <label>确认密码<input v-model="password.confirmPassword" type="password" autocomplete="new-password" required minlength="10" maxlength="128" @input="passwordMismatch = false"></label>
          <p v-if="passwordMismatch" role="alert">两次输入的密码不一致。</p>
          <div class="account-page__actions"><button type="submit" :disabled="busy">修改密码</button></div>
        </form>
      </section>
    </div>
  </main>
</template>

<style scoped>
.account-page { display: grid; min-width: 0; align-content: start; gap: 14px; }
.account-page > header { min-height: 46px; padding: 2px 2px 8px; }
.account-page h1, .account-page h2 { margin: 0; letter-spacing: 0; }
.account-page h1 { font-size: 21px; font-weight: 650; }
.account-page__workspace { display: grid; grid-template-columns: minmax(260px, 300px) minmax(0, 1fr); gap: 14px; }
.account-page__summary, .account-page__detail { min-width: 0; overflow: hidden; background: var(--ga-bg-container); border: 1px solid var(--ga-border-light); border-radius: var(--ga-radius-lg); box-shadow: var(--ga-shadow-sm); }
.account-page__summary > h2, .account-page__detail > h2 { min-height: 52px; padding: 16px 18px; border-bottom: 1px solid var(--ga-border-light); font-size: 15px; font-weight: 650; }
.account-page__identity { display: grid; justify-items: center; gap: 6px; padding: 24px 18px 18px; text-align: center; }
.account-page__identity strong { margin-top: 4px; font-size: 15px; }
.account-page__identity > span:last-child { color: var(--ga-text-3); font-size: 12px; }
.account-page__avatar { display: grid; width: 76px; height: 76px; place-items: center; color: #fff; background: var(--ga-brand); border: 5px solid var(--ga-brand-soft); border-radius: 50%; box-shadow: 0 6px 16px color-mix(in srgb, var(--ga-brand), transparent 76%); font-size: 26px; font-weight: 700; }
.account-page dl { margin: 0; padding: 0 18px; }
.account-page dl > div { display: flex; min-height: 46px; align-items: center; justify-content: space-between; gap: 12px; border-top: 1px solid var(--ga-border-light); font-size: 13px; }
.account-page dt { color: var(--ga-text-3); }
.account-page dd { margin: 0; overflow-wrap: anywhere; color: var(--ga-text-1); font-weight: 550; text-align: right; }
.account-page__logout { width: calc(100% - 36px); margin: 16px 18px 18px; color: var(--ga-danger) !important; background: var(--ga-danger-soft) !important; border-color: color-mix(in srgb, var(--ga-danger), transparent 68%) !important; }
.account-page__detail > .tabs { display: flex; gap: 16px; margin: 0 18px; border-bottom: 1px solid var(--ga-border-light); }
.account-page__detail > .tabs button { min-height: 44px; padding: 8px 2px; border: 0; border-bottom: 2px solid transparent; border-radius: 0; background: transparent; box-shadow: none; }
.account-page__detail > .tabs button:hover { background: transparent; }
.account-page__detail > .tabs button[aria-selected="true"] { color: var(--ga-brand); border-bottom-color: var(--ga-brand); }
.account-page form { display: grid; width: min(100%, 620px); gap: 17px; padding: 22px 18px; }
.account-page label { display: grid; gap: 7px; }
.account-page label input { width: 100%; }
.account-page__actions { display: flex; justify-content: flex-end; padding-top: 4px; }
.account-page__actions button { min-width: 100px; color: #fff !important; background: var(--ga-brand) !important; border-color: var(--ga-brand) !important; }
.account-page form [role="alert"] { margin: 0; }
@media (max-width: 760px) {
  .account-page__workspace { grid-template-columns: 1fr; }
  .account-page__identity { grid-template-columns: auto 1fr; justify-items: start; text-align: left; }
  .account-page__avatar { width: 58px; height: 58px; grid-row: 1 / 3; font-size: 20px; }
  .account-page__detail > .tabs { overflow-x: auto; }
}
</style>
