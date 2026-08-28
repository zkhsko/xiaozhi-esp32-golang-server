import { createRouter, createWebHistory } from 'vue-router'
import DeviceCredentials from '../views/DeviceCredentials.vue'

const routes = [
  {
    path: '/',
    redirect: '/device-credentials',
  },
  {
    path: '/device-credentials',
    name: 'DeviceCredentials',
    component: DeviceCredentials,
    meta: {
      title: '设备生产凭证管理',
    },
  },
]

const router = createRouter({
  history: createWebHistory('/admin/'),
  routes,
})

export default router
