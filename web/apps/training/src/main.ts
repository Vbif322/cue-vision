import { createApp } from 'vue'

import { setUnauthorizedHandler } from './api'
import App from './App.vue'
import { router } from './router'
import { forgetSession } from './session'
import './style.css'

// Сессия истекла или её закрыли (смена пароля, удаление пользователя) — на вход,
// с возвратом на ту же страницу.
setUnauthorizedHandler(() => {
  forgetSession()
  const current = router.currentRoute.value
  if (current.name !== 'login') void router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

createApp(App).use(router).mount('#app')
