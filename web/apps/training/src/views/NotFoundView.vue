<script setup lang="ts">
import { RouterLink } from 'vue-router'
</script>

<template>
  <section>
    <h1>Страница не найдена</h1>
    <RouterLink :to="{ name: 'trainings' }">К списку тренировок</RouterLink>
  </section>
</template>
