<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink } from 'vue-router'

import { trainingsApi, type TrainingStatus } from '@/api'
import { useAsync } from '@/composables/useAsync'
import { formatDateTime, formatPercent } from '@/lib/format'

const status = ref<TrainingStatus | ''>('')

const { data, error, loading } = useAsync(
  (signal) => trainingsApi.list({ status: status.value || undefined, limit: 50 }, signal),
  [status],
)
</script>

<template>
  <section>
    <div class="head">
      <h1>Тренировки</h1>
      <select v-model="status" aria-label="Статус">
        <option value="">Все</option>
        <option value="active">Идут</option>
        <option value="finished">Завершены</option>
      </select>
    </div>

    <p v-if="error" class="error">Не удалось загрузить: {{ error.message }}</p>
    <p v-else-if="loading && !data" class="muted">Загрузка…</p>
    <p v-else-if="data && data.items.length === 0" class="muted">Тренировок пока нет.</p>

    <table v-else-if="data">
      <thead>
        <tr>
          <th>Тренировка</th>
          <th>Игрок</th>
          <th>Стол</th>
          <th>Начало</th>
          <th>Удары</th>
          <th>Реализация</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="t in data.items" :key="t.id">
          <td>
            <RouterLink :to="{ name: 'training', params: { id: t.id } }">
              {{ t.title || `Тренировка #${t.id}` }}
            </RouterLink>
            <span v-if="t.status === 'active'" class="badge">идёт</span>
            <span v-if="t.origin === 'auto'" class="muted"> · авто</span>
          </td>
          <td>{{ t.player_name ?? '—' }}</td>
          <td>{{ t.stream }}</td>
          <td>{{ formatDateTime(t.started_at) }}</td>
          <td>{{ t.pots }} / {{ t.shots }}</td>
          <td>{{ formatPercent(t.shots ? t.pots / t.shots : null) }}</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.badge {
  margin-left: 8px;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 12px;
  color: var(--surface);
  background: var(--accent);
}
@media (max-width: 640px) {
  th:nth-child(3),
  td:nth-child(3),
  th:nth-child(4),
  td:nth-child(4) {
    display: none;
  }
}
</style>
