<script setup lang="ts">
import { vModalFocus } from '@go-admin-plus/ui'
import { PencilIcon, PlusIcon, PowerIcon, RefreshCwIcon, RotateCcwIcon, SearchIcon, Trash2Icon, XIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { Menu, Role, User } from '@go-admin-plus/domain-iam/administration'
import { StatusTag } from '@go-admin-plus/ui/components'
import { createUserAndClearPassword, resetPasswordAndClear, settleAdministrationPageOperation, type AdministrationController, type CreateRoleModel, type CreateUserModel } from './administration-controller'
import { administrationViewForPath } from './administration-view'

const props = defineProps<{ controller: AdministrationController }>()
const emit = defineEmits<{ sessionRequired: [] }>()
const route = useRoute()
const view = computed(() => administrationViewForPath(route.path))
const revision = ref(0)
const pendingOperations = ref(0)
const filters = reactive({ search: '' })
const user = reactive<CreateUserModel>({ username: '', displayName: '', email: '', password: '' })
const role = reactive<CreateRoleModel>({ key: '', name: '', dataScope: 'self' })
const menu = reactive({ key: '', label: '', path: '', permissionCode: '', sortOrder: 0, kind: 'directory' as 'directory' | 'page' | 'button', parentId: '', enabled: true, hidden: false, icon: '' })
const createUserOpen = ref(false)
const createRoleOpen = ref(false)
const createMenuOpen = ref(false)
const editedUser = ref<User | null>(null)
const editedRole = ref<Role | null>(null)
const editedMenu = ref<Menu | null>(null)
const assignedRoles = ref<string[]>([])
const grantedPermissions = ref<string[]>([])
const grantedMenus = ref<string[]>([])
const replacementPassword = ref('')
const deletionUser = ref<User | null>(null)
const deletionStrategy = ref<'transfer' | 'purge'>('transfer')
const transferTargetId = ref('')
const purgeConfirmed = ref(false)
const failureMessage = ref('')
const mutationBlocked = computed(() => { void revision.value; return pendingOperations.value > 0 || props.controller.busy || props.controller.hasPendingRepair() })
const users = computed(() => { void revision.value; return props.controller.users.snapshot() })
const roles = computed(() => { void revision.value; return props.controller.roles() })
const menus = computed(() => { void revision.value; return props.controller.menus() })
const deletion = computed(() => { void revision.value; return props.controller.deletion() })
const can = (permissionCode: string) => { void revision.value; return props.controller.can(permissionCode) }
const surfaceFailure = () => {
  const failure = props.controller.failure()
  failureMessage.value = failure === 'relogin' ? '登录状态已失效，请重新登录。'
    : failure === 'forbidden' ? '当前账号没有执行该操作的权限。'
      : failure === 'validation' ? '请检查提交内容。'
        : failure === 'not-found' ? '请求的用户或删除任务已不存在。'
          : failure === 'conflict' ? '数据已发生变化或受系统保护。'
            : failure === 'unavailable' ? '管理服务暂时不可用。' : ''
  if (failure === 'relogin') emit('sessionRequired')
}
// 页面加载与写入共用可观察的忙碌状态，避免刷新尚未完成时打开新表单。
const run = async <T,>(operation: () => Promise<T>) => {
  pendingOperations.value += 1
  try {
    return await settleAdministrationPageOperation(operation, () => {
      surfaceFailure(); revision.value += 1
    })
  } finally { pendingOperations.value -= 1 }
}
const closeUserEditor = () => { editedUser.value = null; assignedRoles.value = []; replacementPassword.value = '' }
const closeRoleEditor = () => { editedRole.value = null; grantedPermissions.value = []; grantedMenus.value = [] }
const closeMenuEditor = () => { editedMenu.value = null }
const closeDeletion = () => {
  deletionUser.value = null; deletionStrategy.value = 'transfer'; transferTargetId.value = ''; purgeConfirmed.value = false
  props.controller.clearDeletion()
}
const resetUserForm = () => { Object.assign(user, { username: '', displayName: '', email: '', password: '' }) }
const openCreateUser = () => { resetUserForm(); createUserOpen.value = true }
const closeCreateUser = () => { createUserOpen.value = false; resetUserForm() }
const resetRoleForm = () => { Object.assign(role, { key: '', name: '', dataScope: 'self' }) }
const openCreateRole = () => { resetRoleForm(); createRoleOpen.value = true }
const closeCreateRole = () => { createRoleOpen.value = false; resetRoleForm() }
const resetMenuForm = () => { Object.assign(menu, { key: '', label: '', path: '', permissionCode: '', sortOrder: 0, kind: 'directory' as 'directory' | 'page' | 'button', parentId: '', enabled: true, hidden: false, icon: '' }) }
const openCreateMenu = () => { resetMenuForm(); createMenuOpen.value = true }
const closeCreateMenu = () => { createMenuOpen.value = false; resetMenuForm() }
const closeDialogs = () => {
  closeCreateUser(); closeCreateRole(); closeCreateMenu()
  closeUserEditor(); closeRoleEditor(); closeMenuEditor(); closeDeletion()
}
const search = () => run(() => props.controller.users.search({ ...filters }))
const reset = () => run(async () => { filters.search = ''; await props.controller.users.reset() })
const submitUser = async () => {
  const result = await run(() => createUserAndClearPassword(props.controller, { ...user }, () => { user.password = '' }))
  if (result === 'submitted') closeCreateUser()
}
const submitRole = async () => {
  const result = await run(() => props.controller.createRole.run({ ...role }))
  if (result === 'submitted') closeCreateRole()
}
const submitMenu = async () => {
  const result = await run(() => props.controller.createMenu.run({ ...menu }))
  if (result === 'submitted') closeCreateMenu()
}
const editUser = (value: User) => { editedUser.value = { ...value, roleIds: [...value.roleIds] }; assignedRoles.value = [...value.roleIds]; replacementPassword.value = '' }
const saveUser = async () => {
  if (!editedUser.value) return
  if (await run(() => props.controller.updateUser(editedUser.value!, !editedUser.value!.disabled)) === 'completed') closeUserEditor()
}
const toggleUser = (value: User) => run(() => props.controller.updateUser(value, value.disabled))
const saveUserRoles = async () => {
  if (!editedUser.value) return
  if (await run(() => props.controller.setUserRoles(editedUser.value!.id, assignedRoles.value)) === 'completed') closeUserEditor()
}
const resetPassword = async () => {
  if (!editedUser.value) return
  if (await run(() => resetPasswordAndClear(props.controller, editedUser.value!.id, replacementPassword.value, () => { replacementPassword.value = '' })) === 'completed') closeUserEditor()
}
const editRole = (value: Role) => {
  editedRole.value = { ...value, permissionCodes: [...value.permissionCodes], menuIds: [...value.menuIds] }
  grantedPermissions.value = [...value.permissionCodes]; grantedMenus.value = [...value.menuIds]
}
const saveRole = async () => {
  if (!editedRole.value) return
  if (await run(() => props.controller.updateRole(editedRole.value!)) === 'completed') closeRoleEditor()
}
const saveRoleGrants = async () => {
  if (!editedRole.value) return
  if (await run(() => props.controller.setRoleGrants(editedRole.value!.id, grantedPermissions.value, grantedMenus.value)) === 'completed') closeRoleEditor()
}
const openDeletion = (value: User) => {
  props.controller.clearDeletion(); deletionUser.value = value; deletionStrategy.value = 'transfer'; transferTargetId.value = ''; purgeConfirmed.value = false
}
const startDeletion = async () => {
  if (!deletionUser.value) return
  await run(() => props.controller.startUserDeletion(deletionUser.value!.id, {
    strategy: deletionStrategy.value,
    ...(deletionStrategy.value === 'transfer' ? { transferTargetId: transferTargetId.value } : {}),
    purgeConfirmed: deletionStrategy.value === 'purge' && purgeConfirmed.value,
  }))
}
const refreshDeletion = () => deletionUser.value
  ? run(() => props.controller.refreshUserDeletion(deletionUser.value!.id))
  : Promise.resolve()
const cancelDeletion = async () => {
  if (!deletionUser.value || deletion.value?.status !== 'queued') return
  if (await run(() => props.controller.cancelUserDeletion(deletionUser.value!.id)) === 'completed') closeDeletion()
}
const editMenu = (value: Menu) => { editedMenu.value = { ...value } }
const saveMenu = async () => {
  if (!editedMenu.value) return
  if (await run(() => props.controller.updateMenu(editedMenu.value!)) === 'completed') closeMenuEditor()
}
const loadView = () => run(async () => {
  await props.controller.refreshAuthorizationData()
  if (view.value === 'users' && can('iam.users.read')) await props.controller.users.refresh()
})
onMounted(loadView)
watch(view, () => { closeDialogs(); void loadView() })
onBeforeUnmount(() => { closeDialogs(); user.password = ''; replacementPassword.value = '' })
</script>

<template>
  <main class="administration-page ga-work-page" :aria-busy="pendingOperations > 0">
    <header><h1>用户与权限</h1></header>
    <p v-if="failureMessage" role="alert">{{ failureMessage }}</p>
    <button v-if="controller.hasPendingRepair()" type="button" data-testid="repair-projection" :disabled="controller.busy" @click="run(() => controller.repairProjection())"><RefreshCwIcon :size="15" aria-hidden="true" />刷新已保存数据</button>
    <section v-if="view === 'users' && can('iam.users.read')" aria-labelledby="users-heading">
      <h2 id="users-heading">用户管理</h2>
      <form class="toolbar" data-testid="user-search" @submit.prevent="search">
        <label>关键字<input v-model.trim="filters.search" maxlength="100" placeholder="用户名、姓名或邮箱"></label>
        <button type="submit"><SearchIcon :size="15" aria-hidden="true" />查询</button><button type="button" data-testid="user-search-reset" @click="reset"><RotateCcwIcon :size="15" aria-hidden="true" />重置</button>
        <button v-if="can('iam.users.write')" type="button" data-testid="open-create-user" :disabled="mutationBlocked" @click="openCreateUser"><PlusIcon :size="15" aria-hidden="true" />新增</button>
      </form>
      <div class="table-scroll">
        <table><thead><tr><th scope="col">用户名</th><th scope="col">姓名</th><th scope="col">邮箱</th><th scope="col">状态</th><th scope="col">操作</th></tr></thead>
          <tbody><tr v-for="row in users.rows" :key="row.id" :data-row-key="row.username"><td><strong>{{ row.username }}</strong></td><td>{{ row.displayName }}</td><td>{{ row.email }}</td><td><StatusTag :tone="row.disabled ? 'neutral' : 'success'" :label="row.disabled ? '停用' : '启用'" /></td><td><button v-if="can('iam.users.write')" type="button" data-action="edit" :disabled="mutationBlocked" @click="editUser(row)"><PencilIcon :size="14" aria-hidden="true" />编辑</button><button v-if="can('iam.users.write')" type="button" data-action="toggle" :disabled="mutationBlocked" @click="toggleUser(row)"><PowerIcon :size="14" aria-hidden="true" />{{ row.disabled ? '启用' : '停用' }}</button><button v-if="can('iam.users.delete')" type="button" data-action="delete" :disabled="mutationBlocked" @click="openDeletion(row)"><Trash2Icon :size="14" aria-hidden="true" />删除</button></td></tr></tbody>
        </table>
      </div>
      <div class="pagination" data-testid="iam-users-pagination"><span>共 {{ users.total }} 条</span><button type="button" :disabled="mutationBlocked || users.page <= 1" @click="run(() => controller.users.setPage(users.page - 1))">上一页</button><span>第 {{ users.page }} 页</span><label>每页<select :value="users.pageSize" :disabled="mutationBlocked" @change="run(() => controller.users.setPageSize(Number(($event.target as HTMLSelectElement).value)))"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option></select></label><button type="button" :disabled="mutationBlocked || users.page * users.pageSize >= users.total" @click="run(() => controller.users.setPage(users.page + 1))">下一页</button></div>
      <div v-if="createUserOpen && can('iam.users.write')" class="management-dialog-backdrop" @click.self="closeCreateUser" @keydown.esc="closeCreateUser">
        <form class="management-dialog" data-testid="create-user" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="create-user-title" @submit.prevent="submitUser">
          <header class="management-dialog__header"><h3 id="create-user-title">新增用户</h3><button type="button" aria-label="关闭" :disabled="mutationBlocked" @click="closeCreateUser"><XIcon :size="18" aria-hidden="true" /></button></header>
          <div class="management-dialog__body"><label>用户名<input name="username" v-model.trim="user.username" autofocus required minlength="3" maxlength="64"></label><label>姓名<input name="displayName" v-model.trim="user.displayName" required maxlength="80"></label><label>邮箱<input name="email" v-model.trim="user.email" type="email" required></label><label>密码<input name="password" v-model="user.password" type="password" required minlength="10" autocomplete="new-password"></label></div>
          <footer class="management-dialog__footer"><button type="button" :disabled="mutationBlocked" @click="closeCreateUser">取消</button><button type="submit" :disabled="controller.createUser.busy || mutationBlocked">确定</button></footer>
        </form>
      </div>
      <div v-if="editedUser" class="management-dialog-backdrop" @click.self="closeUserEditor" @keydown.esc="closeUserEditor">
        <div class="management-dialog management-dialog--wide" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="edit-user-title">
          <header class="management-dialog__header"><h3 id="edit-user-title">编辑用户 {{ editedUser.username }}</h3><button type="button" aria-label="关闭" :disabled="mutationBlocked" @click="closeUserEditor">×</button></header>
          <div class="management-dialog__body management-dialog__body--sections">
            <form v-if="can('iam.users.write')" class="management-dialog__section" data-testid="edit-user" @submit.prevent="saveUser"><h4>基本信息</h4><label>姓名<input name="displayName" v-model.trim="editedUser.displayName" required maxlength="80"></label><label>邮箱<input name="email" v-model.trim="editedUser.email" type="email" required></label><button type="submit" :disabled="mutationBlocked">保存用户</button></form>
            <form v-if="can('iam.roles.assign')" class="management-dialog__section" data-testid="assign-user-roles" @submit.prevent="saveUserRoles"><h4>分配角色</h4><div class="management-dialog__choices"><label v-for="item in roles" :key="item.id" class="choice"><input v-model="assignedRoles" type="checkbox" :value="item.id" :data-role-key="item.key">{{ item.name }}</label></div><button type="submit" :disabled="mutationBlocked">保存角色分配</button></form>
            <form v-if="can('iam.users.reset-password')" class="management-dialog__section" data-testid="reset-user-password" @submit.prevent="resetPassword"><h4>重置密码</h4><label>新密码<input name="password" v-model="replacementPassword" type="password" required minlength="10" autocomplete="new-password"></label><button type="submit" :disabled="mutationBlocked">重置密码</button></form>
          </div>
        </div>
      </div>
      <div v-if="deletionUser" class="management-dialog-backdrop" @click.self="closeDeletion" @keydown.esc="closeDeletion">
        <form class="management-dialog" data-testid="delete-user" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="delete-user-title" @submit.prevent="startDeletion">
          <header class="management-dialog__header"><h3 id="delete-user-title">删除用户 {{ deletionUser.username }}</h3><button type="button" aria-label="关闭" :disabled="mutationBlocked" @click="closeDeletion">×</button></header>
          <div class="management-dialog__body">
            <fieldset><legend>数据处置</legend><label class="choice"><input v-model="deletionStrategy" type="radio" value="transfer">转移数据后删除</label><label class="choice"><input v-model="deletionStrategy" type="radio" value="purge">永久清除数据</label></fieldset>
            <label v-if="deletionStrategy === 'transfer'">转移到<select v-model="transferTargetId" required><option value="" disabled>选择接收用户</option><option v-for="candidate in users.rows.filter((item) => item.id !== deletionUser!.id)" :key="candidate.id" :value="candidate.id">{{ candidate.displayName }}（{{ candidate.username }}）</option></select></label>
            <label v-else class="choice deletion-warning"><input v-model="purgeConfirmed" type="checkbox" name="purgeConfirmed" required>确认永久清除该用户关联数据，提交后可能无法撤销</label>
            <div v-if="deletion" class="deletion-status" aria-live="polite"><strong>删除状态：{{ deletion.status }}</strong><span>审计编号：{{ deletion.auditReference }}</span><span v-if="deletion.status === 'claimed'">后台任务已领取，不能再取消。</span></div>
          </div>
          <footer class="management-dialog__footer"><button type="button" :disabled="mutationBlocked || controller.deletionLoading()" @click="refreshDeletion">刷新状态</button><button v-if="deletion?.status === 'queued'" type="button" :disabled="mutationBlocked" @click="cancelDeletion">取消删除任务</button><button type="submit" :disabled="mutationBlocked || Boolean(deletion) || (deletionStrategy === 'transfer' ? !transferTargetId : !purgeConfirmed)">提交删除任务</button></footer>
        </form>
      </div>
    </section>

    <section v-else-if="view === 'roles' && can('iam.roles.read')" aria-labelledby="roles-heading">
      <h2 id="roles-heading">角色管理</h2>
      <div class="toolbar management-toolbar"><button v-if="can('iam.roles.write')" type="button" data-testid="open-create-role" :disabled="mutationBlocked" @click="openCreateRole"><PlusIcon :size="15" aria-hidden="true" />新增</button></div>
      <div class="table-scroll"><table><thead><tr><th>角色标识</th><th>角色名称</th><th>数据范围</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="item in roles" :key="item.id" :data-row-key="item.key"><td>{{ item.key }}</td><td>{{ item.name }}</td><td>{{ item.dataScope === 'all' ? '全部' : '本人' }}</td><td><StatusTag :tone="item.enabled ? 'success' : 'neutral'" :label="item.enabled ? '启用' : '停用'" /></td><td><button v-if="can('iam.roles.write')" type="button" data-action="edit" :disabled="mutationBlocked" :aria-label="`编辑角色 ${item.key}`" @click="editRole(item)"><PencilIcon :size="14" aria-hidden="true" />编辑</button><button v-if="can('iam.roles.delete')" type="button" data-action="delete" :aria-label="`删除角色 ${item.key}`" :disabled="item.protected || mutationBlocked" @click="run(() => controller.deleteRole(item.id))"><Trash2Icon :size="14" aria-hidden="true" />删除</button></td></tr></tbody></table></div>
      <div v-if="createRoleOpen && can('iam.roles.write')" class="management-dialog-backdrop" @click.self="closeCreateRole" @keydown.esc="closeCreateRole"><form class="management-dialog" data-testid="create-role" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="create-role-title" @submit.prevent="submitRole"><header class="management-dialog__header"><h3 id="create-role-title">新增角色</h3><button type="button" aria-label="关闭" @click="closeCreateRole">×</button></header><div class="management-dialog__body"><label>角色标识<input name="key" v-model.trim="role.key" autofocus required minlength="3" maxlength="64" pattern="[a-z0-9][a-z0-9_-]*"></label><label>角色名称<input name="name" v-model.trim="role.name" required maxlength="100"></label><label>数据范围<select name="dataScope" v-model="role.dataScope"><option value="self">本人</option><option value="all">全部</option></select></label></div><footer class="management-dialog__footer"><button type="button" @click="closeCreateRole">取消</button><button type="submit" :disabled="controller.createRole.busy || mutationBlocked">确定</button></footer></form></div>
      <div v-if="editedRole" class="management-dialog-backdrop" @click.self="closeRoleEditor" @keydown.esc="closeRoleEditor"><div class="management-dialog management-dialog--wide" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="edit-role-title"><header class="management-dialog__header"><h3 id="edit-role-title">编辑角色 {{ editedRole.key }}</h3><button type="button" aria-label="关闭" @click="closeRoleEditor">×</button></header><div class="management-dialog__body management-dialog__body--sections"><form v-if="can('iam.roles.write')" class="management-dialog__section" data-testid="edit-role" @submit.prevent="saveRole"><h4>基本信息</h4><label>角色名称<input name="name" v-model.trim="editedRole.name" required maxlength="100"></label><label>数据范围<select name="dataScope" v-model="editedRole.dataScope"><option value="all">全部数据</option><option value="self">本人数据</option></select></label><label class="choice"><input name="enabled" v-model="editedRole.enabled" type="checkbox">启用</label><button type="submit" :disabled="editedRole.protected || mutationBlocked">保存角色</button></form><form v-if="can('iam.roles.assign')" class="management-dialog__section" data-testid="assign-role-grants" @submit.prevent="saveRoleGrants"><h4>分配权限与菜单</h4><fieldset><legend>权限标识</legend><label v-for="item in controller.permissions()" :key="item.code" class="choice"><input v-model="grantedPermissions" type="checkbox" :value="item.code" :data-permission-code="item.code">{{ item.name }}</label></fieldset><fieldset><legend>菜单</legend><label v-for="item in menus" :key="item.id" class="choice"><input v-model="grantedMenus" type="checkbox" :value="item.id" :data-menu-key="item.key">{{ item.label }}</label></fieldset><button type="submit" :disabled="editedRole.protected || mutationBlocked">保存权限分配</button></form></div></div></div>
    </section>

    <section v-else-if="view === 'menus' && can('iam.menus.read')" aria-labelledby="menus-heading">
      <h2 id="menus-heading">菜单管理</h2>
      <div class="toolbar management-toolbar"><button v-if="can('iam.menus.write')" type="button" data-testid="open-create-menu" :disabled="mutationBlocked" @click="openCreateMenu"><PlusIcon :size="15" aria-hidden="true" />新增</button></div>
      <div class="table-scroll"><table><thead><tr><th>菜单标识</th><th>菜单名称</th><th>类型 / 父级</th><th>路由地址</th><th>权限标识</th><th>操作</th></tr></thead><tbody><tr v-for="item in menus" :key="item.id" :data-row-key="item.key"><td>{{ item.key }}</td><td>{{ item.label }}</td><td>{{ item.kind === 'directory' ? '目录' : item.kind === 'button' ? '按钮' : '页面' }} / {{ menus.find(v => v.id === item.parentId)?.label || '根目录' }}</td><td>{{ item.path }}</td><td>{{ item.permissionCode }}</td><td><button v-if="can('iam.menus.write')" type="button" data-action="edit" :disabled="mutationBlocked" @click="editMenu(item)"><PencilIcon :size="14" aria-hidden="true" />编辑</button><button v-if="can('iam.menus.delete')" type="button" data-action="delete" :disabled="item.protected || mutationBlocked" @click="run(() => controller.deleteMenu(item.id))"><Trash2Icon :size="14" aria-hidden="true" />删除</button></td></tr></tbody></table></div>
      <div v-if="createMenuOpen && can('iam.menus.write')" class="management-dialog-backdrop" @click.self="closeCreateMenu" @keydown.esc="closeCreateMenu"><form class="management-dialog" data-testid="create-menu" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="create-menu-title" @submit.prevent="submitMenu"><header class="management-dialog__header"><h3 id="create-menu-title">新增菜单</h3><button type="button" aria-label="关闭" @click="closeCreateMenu">×</button></header><div class="management-dialog__body"><label>菜单标识<input name="key" v-model.trim="menu.key" autofocus required minlength="3" maxlength="64" pattern="[a-z0-9][a-z0-9_-]*"></label><label>菜单名称<input name="label" v-model.trim="menu.label" required maxlength="80"></label><label>类型<select name="kind" v-model="menu.kind" ><option value="directory">目录</option><option value="page">页面</option><option value="button">按钮</option></select></label><label>父级<select name="parentId" v-model="menu.parentId"><option value="">根目录</option><option v-for="parent in menus.filter(v => v.kind !== 'button' && v.id !== '')" :key="parent.id" :value="parent.id">{{ parent.label }}</option></select></label><label v-if="menu.kind === 'page'">注册页面<select name="path" v-model="menu.path" required><option value="">请选择</option><option v-for="page in menus.filter(v => v.protected && v.kind === 'page')" :key="page.id" :value="page.path">{{ page.label }}（{{ page.path }}）</option></select></label><label class="choice"><input v-model="menu.enabled" type="checkbox">启用</label><label class="choice"><input v-model="menu.hidden" type="checkbox">隐藏导航</label><label v-if="menu.kind === 'button'">按钮权限<select name="permissionCode" v-model="menu.permissionCode" required><option v-for="p in controller.permissions()" :key="p.code" :value="p.code">{{ p.name }}</option></select></label><label>显示顺序<input name="sortOrder" v-model.number="menu.sortOrder" type="number" min="0" max="100000"></label></div><footer class="management-dialog__footer"><button type="button" @click="closeCreateMenu">取消</button><button type="submit" :disabled="controller.createMenu.busy || mutationBlocked">确定</button></footer></form></div>
      <div v-if="editedMenu && can('iam.menus.write')" class="management-dialog-backdrop" @click.self="closeMenuEditor" @keydown.esc="closeMenuEditor"><form class="management-dialog" data-testid="edit-menu" role="dialog" v-modal-focus aria-modal="true" aria-labelledby="edit-menu-title" @submit.prevent="saveMenu"><header class="management-dialog__header"><h3 id="edit-menu-title">编辑菜单 {{ editedMenu.key }}</h3><button type="button" aria-label="关闭" @click="closeMenuEditor">×</button></header><div class="management-dialog__body"><label>菜单名称<input name="label" v-model.trim="editedMenu.label" required maxlength="80"></label><label>类型<select name="kind" v-model="editedMenu.kind"  :disabled="editedMenu.protected"><option value="directory">目录</option><option value="page">页面</option><option value="button">按钮</option></select></label><label>父级<select name="parentId" v-model="editedMenu.parentId"><option value="">根目录</option><option v-for="parent in menus.filter(v => v.kind !== 'button' && v.id !== editedMenu?.id)" :key="parent.id" :value="parent.id">{{ parent.label }}</option></select></label><label v-if="editedMenu.kind === 'page'">注册页面<select name="path" v-model="editedMenu.path" required :disabled="editedMenu.protected"><option value="">请选择</option><option v-for="page in menus.filter(v => v.protected && v.kind === 'page')" :key="page.id" :value="page.path">{{ page.label }}（{{ page.path }}）</option></select></label><label class="choice"><input v-model="editedMenu.enabled" type="checkbox">启用</label><label class="choice"><input v-model="editedMenu.hidden" type="checkbox">隐藏导航</label><label v-if="editedMenu.kind === 'button'">按钮权限<select name="permissionCode" v-model="editedMenu.permissionCode" required><option v-for="p in controller.permissions()" :key="p.code" :value="p.code">{{ p.name }}</option></select></label><label>显示顺序<input name="sortOrder" v-model.number="editedMenu.sortOrder" type="number" min="0" max="100000"></label></div><footer class="management-dialog__footer"><button type="button" @click="closeMenuEditor">取消</button><button type="submit" :disabled="mutationBlocked">保存</button></footer></form></div>
    </section>
    <section v-else aria-live="polite"><p>当前账号没有可用的管理视图。</p></section>
  </main>
</template>

<style scoped>
.administration-page { display: grid; gap: 14px; }
h1, h2, h3 { margin: 0; letter-spacing: 0; }
.toolbar, .pagination { display: flex; align-items: end; gap: 8px; flex-wrap: wrap; }
section { display: grid; min-width: 0; gap: 16px; }
section > h2 { padding: 0 2px; font-size: 16px; font-weight: 650; }
label { display: grid; gap: 6px; }
.table-scroll { width: 100%; min-width: 0; overflow-x: auto; background: var(--ga-bg-container); border: 1px solid var(--ga-border-light); border-radius: var(--ga-radius-lg); box-shadow: var(--ga-shadow-sm); }
.table-scroll table { border: 0; border-radius: 0; box-shadow: none; }
.pagination label { display: flex; align-items: center; gap: 6px; white-space: nowrap; }
.deletion-warning { color: var(--el-color-danger); }
.deletion-status { display: grid; gap: 4px; padding: 12px; border: 1px solid var(--el-border-color); border-radius: var(--ga-radius); background: var(--el-fill-color-light); }
@media (max-width: 640px) {
  [data-testid="user-search"] > label { flex: 1 0 100%; }
  [data-testid="user-search"] > button { flex: 1 1 0; padding-inline: 8px; }
  .pagination { justify-content: flex-start; gap: 6px; }
  .pagination button { min-height: 32px; padding-inline: 9px; }
  .pagination > span:first-child { margin-right: auto; }
}
</style>
