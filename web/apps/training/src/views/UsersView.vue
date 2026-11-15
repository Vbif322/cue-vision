<script setup lang="ts">
import { reactive, ref } from 'vue'

import { ApiError, usersApi, type Role, type User } from '@/api'
import { useAsync } from '@/composables/useAsync'
import { formatDateTime } from '@/lib/format'
import { useSession } from '@/session'

const { user: me } = useSession()
const { data, error, loading, reload } = useAsync((signal) => usersApi.list(signal))

const roleLabel: Record<Role, string> = { admin: 'администратор', staff: 'сотрудник' }

const message = (err: unknown) => (err instanceof ApiError ? err.message : String(err))

// --- новый пользователь ---

const draft = reactive({ login: '', password: '', role: 'staff' as Role })
const createError = ref('')
const creating = ref(false)

async function create() {
  createError.value = ''
  rowError.value = undefined
  creating.value = true
  try {
    await usersApi.create({ ...draft })
    draft.login = draft.password = ''
    draft.role = 'staff'
    await reload()
  } catch (err) {
    createError.value = message(err)
  } finally {
    creating.value = false
  }
}

// --- действия над строкой ---

const rowError = ref<{ id: number; text: string }>()
const resetFor = ref<number>()
const resetPassword = ref('')
const busy = ref<number>()

async function act(u: User, action: () => Promise<unknown>) {
  rowError.value = undefined
  busy.value = u.id
  try {
    await action()
    await reload()
    return true
  } catch (err) {
    rowError.value = { id: u.id, text: message(err) }
    return false
  } finally {
    busy.value = undefined
  }
}

function changeRole(u: User, role: Role) {
  if (role !== u.role) void act(u, () => usersApi.update(u.id, { role }))
}

function startReset(u: User) {
  resetFor.value = resetFor.value === u.id ? undefined : u.id
  resetPassword.value = ''
  rowError.value = undefined
}

async function submitReset(u: User) {
  if (await act(u, () => usersApi.update(u.id, { password: resetPassword.value }))) {
    resetFor.value = undefined
    resetPassword.value = ''
  }
}

function remove(u: User) {
  if (window.confirm(`Удалить пользователя ${u.login}?`)) void act(u, () => usersApi.remove(u.id))
}
</script>

<template>
  <section>
    <h1>Пользователи</h1>

    <form class="create" @submit.prevent="create">
      <label class="field">
        <span>Логин</span>
        <input v-model="draft.login" autocomplete="off" autocapitalize="none" pattern="[A-Za-z0-9][A-Za-z0-9._\-]{1,63}" required />
      </label>
      <label class="field">
        <span>Пароль, от 8 символов</span>
        <input v-model="draft.password" type="password" autocomplete="new-password" minlength="8" required />
      </label>
      <label class="field">
        <span>Роль</span>
        <select v-model="draft.role">
          <option value="staff">{{ roleLabel.staff }}</option>
          <option value="admin">{{ roleLabel.admin }}</option>
        </select>
      </label>
      <button type="submit" :disabled="creating">Добавить</button>
    </form>
    <p v-if="createError" class="error" role="alert">{{ createError }}</p>
    <p class="muted">
      Логин: латинские буквы, цифры, точка, дефис, подчёркивание. Сотрудник видит и ведёт тренировки, администратор ещё
      и заводит пользователей.
    </p>

    <p v-if="error" class="error">Не удалось загрузить: {{ error.message }}</p>
    <p v-else-if="loading && !data" class="muted">Загрузка…</p>

    <table v-else-if="data">
      <thead>
        <tr>
          <th>Логин</th>
          <th>Роль</th>
          <th>Заведён</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <template v-for="u in data.items" :key="u.id">
          <tr>
            <td>
              {{ u.login }}
              <span v-if="u.id === me?.id" class="muted"> · это вы</span>
            </td>
            <td>
              <select
                :value="u.role"
                :disabled="busy === u.id"
                aria-label="Роль"
                @change="changeRole(u, ($event.target as HTMLSelectElement).value as Role)"
              >
                <option value="staff">{{ roleLabel.staff }}</option>
                <option value="admin">{{ roleLabel.admin }}</option>
              </select>
            </td>
            <td>{{ formatDateTime(u.created_at) }}</td>
            <td>
              <div class="actions">
                  <button type="button" class="secondary" :disabled="busy === u.id" @click="startReset(u)">Пароль</button>
                <button v-if="u.id !== me?.id" type="button" class="danger" :disabled="busy === u.id" @click="remove(u)">
                  Удалить
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="resetFor === u.id">
            <td colspan="4">
              <form class="reset" @submit.prevent="submitReset(u)">
                <input
                  v-model="resetPassword"
                  type="password"
                  autocomplete="new-password"
                  minlength="8"
                  required
                  :aria-label="`Новый пароль для ${u.login}`"
                  placeholder="Новый пароль"
                />
                <button type="submit" :disabled="busy === u.id">Задать</button>
                <span class="muted">Все входы {{ u.login }} будут закрыты.</span>
              </form>
            </td>
          </tr>
          <tr v-if="rowError?.id === u.id">
            <td colspan="4" class="error" role="alert">{{ rowError.text }}</td>
          </tr>
        </template>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.create {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 12px;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}
@media (max-width: 640px) {
  th:nth-child(3),
  td:nth-child(3) {
    display: none;
  }
}
.reset {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
</style>
