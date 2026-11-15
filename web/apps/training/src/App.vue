<script setup lang="ts">
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import { signOut, useSession } from '@/session'

const { user, isAdmin } = useSession()
const route = useRoute()
const router = useRouter()

async function logout() {
  try {
    await signOut()
  } finally {
    await router.replace({ name: 'login' })
  }
}
</script>

<template>
  <header class="app-header">
    <RouterLink to="/" class="app-header__brand">cue-vision · тренировки</RouterLink>
    <template v-if="user && !route.meta.public">
      <nav class="app-header__nav">
        <RouterLink :to="{ name: 'trainings' }">Тренировки</RouterLink>
        <RouterLink v-if="isAdmin" :to="{ name: 'users' }">Пользователи</RouterLink>
      </nav>
      <div class="app-header__user">
        <RouterLink :to="{ name: 'account' }" title="Учётная запись">{{ user.login }}</RouterLink>
        <button type="button" class="link-button" @click="logout">Выйти</button>
      </div>
    </template>
  </header>
  <main class="app-main">
    <RouterView />
  </main>
</template>

<style scoped>
.app-header {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 24px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.app-header__brand {
  font-weight: 600;
  color: var(--text-strong);
  text-decoration: none;
}
.app-header__nav {
  display: flex;
  gap: 16px;
}
.app-header__nav a,
.app-header__user a {
  color: var(--text-muted);
  text-decoration: none;
}
.app-header__nav a.router-link-active,
.app-header__user a.router-link-active {
  color: var(--accent);
}
.app-header__user {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-left: auto;
}
.app-main {
  max-width: 1100px;
  margin: 0 auto;
  padding: 24px 16px;
}
</style>
