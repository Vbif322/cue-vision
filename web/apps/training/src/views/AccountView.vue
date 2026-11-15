<script setup lang="ts">
import { ref } from 'vue'

import { ApiError, authApi } from '@/api'
import { formatDateTime } from '@/lib/format'
import { useSession } from '@/session'

const { user } = useSession()

const current = ref('')
const next = ref('')
const repeat = ref('')
const error = ref('')
const done = ref(false)
const submitting = ref(false)

async function submit() {
  error.value = ''
  done.value = false
  if (next.value !== repeat.value) {
    error.value = 'Новый пароль и повтор не совпадают'
    return
  }
  submitting.value = true
  try {
    await authApi.changePassword(current.value, next.value)
    done.value = true
    current.value = next.value = repeat.value = ''
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : String(err)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section v-if="user">
    <h1>{{ user.login }}</h1>
    <p class="muted">
      Роль: {{ user.role === 'admin' ? 'администратор' : 'сотрудник' }} · заведён {{ formatDateTime(user.created_at) }}
    </p>

    <h2>Смена пароля</h2>
    <form class="form" @submit.prevent="submit">
      <label class="field">
        <span>Текущий пароль</span>
        <input v-model="current" type="password" autocomplete="current-password" required />
      </label>
      <label class="field">
        <span>Новый пароль, не короче 8 символов</span>
        <input v-model="next" type="password" autocomplete="new-password" minlength="8" required />
      </label>
      <label class="field">
        <span>Повтор нового пароля</span>
        <input v-model="repeat" type="password" autocomplete="new-password" minlength="8" required />
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="done" class="success">Пароль изменён. Входы на других устройствах закрыты.</p>
      <div>
        <button type="submit" :disabled="submitting">Сменить пароль</button>
      </div>
    </form>
  </section>
</template>
