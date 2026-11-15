<script setup lang="ts">
defineProps<{
  label: string
  value: string | number
  hint?: string
}>()
</script>

<template>
  <div class="tile">
    <div class="tile__label">{{ label }}</div>
    <div class="tile__value">{{ value }}</div>
    <div v-if="hint" class="tile__hint">{{ hint }}</div>
  </div>
</template>

<style scoped>
.tile {
  padding: 12px 16px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface);
}
.tile__label {
  font-size: 13px;
  color: var(--text-muted);
}
.tile__value {
  font-size: 28px;
  font-weight: 600;
  color: var(--text-strong);
  font-variant-numeric: tabular-nums;
}
.tile__hint {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
