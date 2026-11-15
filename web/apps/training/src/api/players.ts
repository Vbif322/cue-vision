import { http } from './http'
import type { List, Player } from './types'

export const playersApi = {
  list: (page: { limit?: number; offset?: number } = {}, signal?: AbortSignal) =>
    http.get<List<Player>>('/players', page, signal),
  get: (id: number, signal?: AbortSignal) => http.get<Player>(`/players/${id}`, undefined, signal),
  create: (name: string) => http.post<Player>('/players', { name }),
}
