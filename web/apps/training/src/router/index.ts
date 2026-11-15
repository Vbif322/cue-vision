import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: { name: 'trainings' } },
    { path: '/trainings', name: 'trainings', component: () => import('@/views/TrainingsView.vue') },
    {
      path: '/trainings/:id(\\d+)',
      name: 'training',
      component: () => import('@/views/TrainingView.vue'),
      props: (route) => ({ id: Number(route.params.id) }),
    },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue') },
  ],
})
