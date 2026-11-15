import { http } from './http'
import type { List, NewTraining, Shot, Training, TrainingPatch, TrainingStats, TrainingStatus } from './types'

export interface TrainingFilter {
  stream?: string
  player_id?: number
  status?: TrainingStatus
  limit?: number
  offset?: number
}

export const trainingsApi = {
  list: (filter: TrainingFilter = {}, signal?: AbortSignal) =>
    http.get<List<Training>>('/trainings', { ...filter }, signal),
  get: (id: number, signal?: AbortSignal) => http.get<Training>(`/trainings/${id}`, undefined, signal),
  start: (input: NewTraining) => http.post<Training>('/trainings', input),
  update: (id: number, patch: TrainingPatch) => http.patch<Training>(`/trainings/${id}`, patch),
  finish: (id: number, endedAt?: string) =>
    http.post<Training>(`/trainings/${id}/finish`, endedAt ? { ended_at: endedAt } : undefined),
  stats: (id: number, signal?: AbortSignal) => http.get<TrainingStats>(`/trainings/${id}/stats`, undefined, signal),
  shots: (id: number, page: { limit?: number; offset?: number } = {}, signal?: AbortSignal) =>
    http.get<List<Shot>>(`/trainings/${id}/shots`, page, signal),
}
