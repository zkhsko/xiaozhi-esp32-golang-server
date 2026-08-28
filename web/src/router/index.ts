import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    component: {
      template: '<div class="dashboard-placeholder"></div>',
    },
  },
]

const router = createRouter({
  history: createWebHistory('/admin/'),
  routes,
})

export default router
