import type { Outcome, Pocket } from '@/api'

const dateTime = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })
const time = new Intl.DateTimeFormat('ru-RU', { timeStyle: 'medium' })

export const formatDateTime = (iso: string | null | undefined) => (iso ? dateTime.format(new Date(iso)) : '—')
export const formatTime = (iso: string | null | undefined) => (iso ? time.format(new Date(iso)) : '—')

export function formatPercent(ratio: number | null | undefined): string {
  return ratio === null || ratio === undefined ? '—' : `${Math.round(ratio * 100)}%`
}

export function formatNumber(value: number | null | undefined, digits = 1): string {
  return value === null || value === undefined ? '—' : value.toFixed(digits)
}

export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return '—'
  const s = Math.round(seconds)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rest = s % 60
  return h > 0 ? `${h} ч ${m} мин` : m > 0 ? `${m} мин ${rest} с` : `${rest} с`
}

export const outcomeLabel: Record<Outcome, string> = {
  pot: 'Забит',
  miss: 'Промах',
  no_contact: 'Без касания',
  in_off: 'Биток в лузе',
}

export const pocketLabel: Record<Pocket, string> = {
  tl: 'Верхняя левая',
  tm: 'Верхняя средняя',
  tr: 'Верхняя правая',
  bl: 'Нижняя левая',
  bm: 'Нижняя средняя',
  br: 'Нижняя правая',
}
