// Кто вошёл. Состояние общее для приложения: шапка, маршрутизатор и страницы
// читают одного и того же пользователя.

import { computed, shallowRef } from 'vue'

import { ApiError, authApi, type User } from '@/api'

const user = shallowRef<User | null>(null)
let loading: Promise<User | null> | undefined

/** Узнаёт у сервера, кто вошёл; результат кешируется до входа или выхода. */
export function loadSession(): Promise<User | null> {
  loading ??= authApi
    .me()
    .then((me) => (user.value = me.user))
    .catch((err: unknown) => {
      loading = undefined
      if (err instanceof ApiError && err.status === 401) return (user.value = null)
      throw err
    })
  return loading
}

export async function signIn(login: string, password: string): Promise<User | null> {
  const me = await authApi.login(login, password)
  user.value = me.user
  loading = Promise.resolve(me.user)
  return me.user
}

export async function signOut(): Promise<void> {
  try {
    await authApi.logout()
  } finally {
    forgetSession()
  }
}

/** Забыть пользователя без запроса к серверу — когда сервер уже ответил 401. */
export function forgetSession() {
  user.value = null
  loading = Promise.resolve(null)
}

export function useSession() {
  return {
    user,
    isAdmin: computed(() => user.value?.role === 'admin'),
  }
}
