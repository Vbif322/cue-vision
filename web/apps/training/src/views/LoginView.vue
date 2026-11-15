<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError } from '@/api'
import { safeRedirect } from '@/router'
import { signIn } from '@/session'

const route = useRoute()
const router = useRouter()

const login = ref('')
const password = ref('')
const error = ref('')
const submitting = ref(false)

async function submit() {
  error.value = ''
  submitting.value = true
  try {
    const user = await signIn(login.value, password.value)
    if (!user) {
      error.value = 'Вход не удался'
      return
    }
    await router.replace(safeRedirect(route.query.redirect))
  } catch (err) {
    error.value =
      err instanceof ApiError ? err.message : 'Сервер недоступен, попробуйте ещё раз'
    password.value = ''
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="login">
    <h1>Вход</h1>
    <form class="form" @submit.prevent="submit">
      <label class="field">
        <span>Логин</span>
        <input v-model="login" name="username" autocomplete="username" autocapitalize="none" required autofocus />
      </label>
      <label class="field">
        <span>Пароль</span>
        <input v-model="password" type="password" name="password" autocomplete="current-password" required />
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div>
        <button type="submit" :disabled="submitting">{{ submitting ? 'Вход…' : 'Войти' }}</button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.login {
  max-width: 360px;
  margin: 48px auto 0;
}
</style>
