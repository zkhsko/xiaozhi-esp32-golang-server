import { createRouter, createWebHistory } from 'vue-router'
import DeviceCredentials from '../views/DeviceCredentials.vue'
import DeviceActivations from '../views/DeviceActivations.vue'
import DeviceTypes from '../views/DeviceTypes.vue'
import ASRConfigs from '../views/ASRConfigs.vue'
import LLMConfigs from '../views/LLMConfigs.vue'
import TTSConfigs from '../views/TTSConfigs.vue'
import AgentConfigs from '../views/AgentConfigs.vue'
import AgentKitConfigs from '../views/AgentKitConfigs.vue'

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
      title: '设备生产管理',
    },
  },
  {
    path: '/device-activations',
    name: 'DeviceActivations',
    component: DeviceActivations,
    meta: {
      title: '设备激活管理',
    },
  },
  {
    path: '/device-types',
    name: 'DeviceTypes',
    component: DeviceTypes,
    meta: {
      title: '设备类型管理',
    },
  },
  {
    path: '/asr-configs',
    name: 'ASRConfigs',
    component: ASRConfigs,
    meta: {
      title: '语音识别配置管理',
    },
  },
  {
    path: '/llm-configs',
    name: 'LLMConfigs',
    component: LLMConfigs,
    meta: {
      title: '大模型配置管理',
    },
  },
  {
    path: '/tts-configs',
    name: 'TTSConfigs',
    component: TTSConfigs,
    meta: {
      title: '语音合成配置管理',
    },
  },
  {
    path: '/agent-configs',
    name: 'AgentConfigs',
    component: AgentConfigs,
    meta: {
      title: '智能体配置管理',
    },
  },
  {
    path: '/agentkit-configs',
    name: 'AgentKitConfigs',
    component: AgentKitConfigs,
    meta: {
      title: '内建工具管理',
    },
  },
]


const router = createRouter({
  history: createWebHistory('/admin/'),
  routes,
})

export default router
