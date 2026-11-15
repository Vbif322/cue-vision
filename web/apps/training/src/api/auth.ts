import { http } from './http'
import type { List, Me, NewUser, User, UserPatch } from './types'

export const authApi = {
  me: (signal?: AbortSignal) => http.get<Me>('/auth/me', undefined, signal),
  login: (login: string, password: string) => http.post<Me>('/auth/login', { login, password }),
  logout: () => http.post<void>('/auth/logout'),
  changePassword: (currentPassword: string, newPassword: string) =>
    http.post<void>('/auth/password', { current_password: currentPassword, new_password: newPassword }),
}

export const usersApi = {
  list: (signal?: AbortSignal) => http.get<List<User>>('/users', { limit: 1000 }, signal),
  create: (input: NewUser) => http.post<User>('/users', input),
  update: (id: number, patch: UserPatch) => http.patch<User>(`/users/${id}`, patch),
  remove: (id: number) => http.delete<void>(`/users/${id}`),
}
