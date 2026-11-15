import { createRouter, createWebHistory } from 'vue-router'

import { loadSession } from '@/session'

declare module 'vue-router' {
  interface RouteMeta {
    /** Страница открыта без входа. */
    public?: boolean
    /** Только для роли admin. */
    admin?: boolean
  }
}

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: { name: 'trainings' } },
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    { path: '/trainings', name: 'trainings', component: () => import('@/views/TrainingsView.vue') },
    {
      path: '/trainings/:id(\\d+)',
      name: 'training',
      component: () => import('@/views/TrainingView.vue'),
      props: (route) => ({ id: Number(route.params.id) }),
    },
    { path: '/account', name: 'account', component: () => import('@/views/AccountView.vue') },
    { path: '/users', name: 'users', component: () => import('@/views/UsersView.vue'), meta: { admin: true } },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue') },
  ],
})

/** Куда вернуться после входа: только путь внутри приложения, не чужой адрес. */
export function safeRedirect(raw: unknown): string {
  return typeof raw === 'string' && raw.startsWith('/') && !raw.startsWith('//') ? raw : '/'
}

router.beforeEach(async (to) => {
  let user
  try {
    user = await loadSession()
  } catch {
    // Сервер недоступен: страница входа покажет ошибку при попытке войти.
    user = null
  }
  if (to.name === 'login') return user ? safeRedirect(to.query.redirect) : true
  if (to.meta.public) return true
  if (!user) return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  if (to.meta.admin && user.role !== 'admin') return { name: 'trainings' }
  return true
})
