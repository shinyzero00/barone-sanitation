import { createRouter, createWebHistory } from 'vue-router'
import { useAccount } from './stores/account'
import CatalogView from './views/CatalogView.vue'

export const router = createRouter({
  history: createWebHistory(), scrollBehavior: () => ({ top: 0 }),
  routes: [
    { path: '/', component: CatalogView },
    { path: '/consultants/:id', component: CatalogView },
    { path: '/:mode(login|register|reset)', name: 'auth', component: () => import('./views/AuthView.vue') },
    { path: '/bookings', name: 'bookings', component: () => import('./views/BookingsView.vue'), meta: { protected: true } },
    { path: '/bookings/:id', name: 'booking', component: () => import('./views/BookingView.vue'), meta: { protected: true } },
    { path: '/reserve', name: 'reserve', component: () => import('./views/ReserveView.vue'), meta: { role: 'client' } },
    { path: '/profile', name: 'profile', component: () => import('./views/ProfileView.vue'), meta: { protected: true } },
    { path: '/admin', name: 'admin', component: () => import('./views/AdminView.vue'), meta: { role: 'admin' } },
    { path: '/:pathMatch(.*)*', component: { template: '<h1>Страница не найдена</h1><a href="/">В каталог</a>' } },
  ],
})

let initialized = false
router.beforeEach(async to => {
  const account = useAccount()
  if (!initialized || to.meta.protected || to.meta.role) {
    try { await account.refresh(); initialized = true } catch { /* Views report request failures. */ }
  }
  if ((to.meta.protected || to.meta.role) && !account.profile) return { path: '/login', query: { next: to.fullPath } }
  if (to.meta.role && to.meta.role !== account.profile?.role) return '/bookings'
})
