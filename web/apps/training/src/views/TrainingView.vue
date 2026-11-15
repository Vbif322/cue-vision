<script setup lang="ts">
import { computed } from 'vue'

import { trainingsApi } from '@/api'
import StatTile from '@/components/StatTile.vue'
import { useAsync } from '@/composables/useAsync'
import { formatDateTime, formatDuration, formatNumber, formatPercent, formatTime, outcomeLabel, pocketLabel } from '@/lib/format'

const props = defineProps<{ id: number }>()

const stats = useAsync((signal) => trainingsApi.stats(props.id, signal), [() => props.id])
const shots = useAsync((signal) => trainingsApi.shots(props.id, { limit: 200 }, signal), [() => props.id])

const training = computed(() => stats.data.value?.training)
const s = computed(() => stats.data.value?.stats)
</script>

<template>
  <section>
    <p v-if="stats.error.value" class="error">Не удалось загрузить: {{ stats.error.value.message }}</p>
    <p v-else-if="!training || !s" class="muted">Загрузка…</p>

    <template v-else>
      <h1>{{ training.title || `Тренировка #${training.id}` }}</h1>
      <p class="muted">
        {{ training.player_name ?? 'Игрок не указан' }} · стол {{ training.stream }} ·
        {{ formatDateTime(training.started_at) }} · {{ formatDuration(stats.data.value?.duration) }}
        <template v-if="training.status === 'active'"> · идёт</template>
      </p>

      <div class="tiles">
        <StatTile label="Удары" :value="s.shots" />
        <StatTile label="Реализация" :value="formatPercent(s.pot_rate)" :hint="`${s.pots} забито`" />
        <StatTile label="Лучшая серия" :value="s.best_streak" />
        <StatTile
          label="Близкие промахи"
          :value="s.near_misses"
          :hint="`слева ${s.near_miss_left} · справа ${s.near_miss_right}`"
        />
        <StatTile label="Биток в лузе" :value="s.in_offs" />
        <StatTile label="Средний угол резки" :value="`${formatNumber(s.avg_cut_angle, 0)}°`" />
      </div>

      <h2>По лузам</h2>
      <table>
        <thead>
          <tr>
            <th>Луза</th>
            <th>Забито</th>
            <th>Близко мимо</th>
            <th>Слева / справа</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in s.by_pocket" :key="p.pocket">
            <td>{{ pocketLabel[p.pocket] }}</td>
            <td>{{ p.pots }}</td>
            <td>{{ p.near_misses }}</td>
            <td>{{ p.near_miss_left }} / {{ p.near_miss_right }}</td>
          </tr>
        </tbody>
      </table>

      <h2>Удары</h2>
      <p v-if="shots.error.value" class="error">Не удалось загрузить удары: {{ shots.error.value.message }}</p>
      <table v-else-if="shots.data.value">
        <thead>
          <tr>
            <th>№</th>
            <th>Время</th>
            <th>Первый контакт</th>
            <th>Забиты</th>
            <th>Исход</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="shot in shots.data.value.items" :key="shot.id" :class="{ muted: shot.status !== 'closed' }">
            <td>{{ shot.shot_no }}</td>
            <td>{{ formatTime(shot.started_at) }}</td>
            <td>{{ shot.first_contact ?? '—' }}</td>
            <td>{{ shot.pocketed.join(', ') || '—' }}</td>
            <td>
              <template v-if="shot.status === 'rejected'">не удар: {{ shot.reject_reason }}</template>
              <template v-else-if="shot.status === 'aborted'">прерван</template>
              <span v-else-if="shot.outcome" :class="`outcome outcome--${shot.outcome}`">
                {{ outcomeLabel[shot.outcome] }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </section>
</template>

<style scoped>
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px;
  margin: 16px 0 24px;
}
.outcome--pot {
  color: var(--pot);
  font-weight: 500;
}
.outcome--miss,
.outcome--in_off {
  color: var(--miss);
}
</style>
