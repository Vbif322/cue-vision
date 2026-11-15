<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'
</script>

<template>
  <header class="app-header">
    <RouterLink to="/" class="app-header__brand">cue-vision · тренировки</RouterLink>
    <nav class="app-header__nav">
      <RouterLink :to="{ name: 'trainings' }">Тренировки</RouterLink>
    </nav>
  </header>
  <main class="app-main">
    <RouterView />
  </main>
</template>

<style scoped>
.app-header {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.app-header__brand {
  font-weight: 600;
  color: var(--text-strong);
  text-decoration: none;
}
.app-header__nav a {
  color: var(--text-muted);
  text-decoration: none;
}
.app-header__nav a.router-link-active {
  color: var(--accent);
}
.app-main {
  max-width: 1100px;
  margin: 0 auto;
  padding: 24px 16px;
}
</style>
