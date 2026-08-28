import { createRouter, createWebHistory } from 'vue-router'
import DeviceCredentials from '../views/DeviceCredentials.vue'
import DeviceActivations from '../views/DeviceActivations.vue'
import ASRConfigs from '../views/ASRConfigs.vue'
import LLMConfigs from '../views/LLMConfigs.vue'
import TTSConfigs from '../views/TTSConfigs.vue'
import AgentConfigs from '../views/AgentConfigs.vue'

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
  {
    path: '/device-activations',
    name: 'DeviceActivations',
    component: DeviceActivations,
    meta: {
      title: '设备激活关系管理',
    },
  },
  {
    path: '/asr-configs',
    name: 'ASRConfigs',
    component: ASRConfigs,
    meta: {
      title: 'ASR 语音识别配置',
    },
  },
  {
    path: '/llm-configs',
    name: 'LLMConfigs',
    component: LLMConfigs,
    meta: {
      title: 'LLM 语言模型配置',
    },
  },
  {
    path: '/tts-configs',
    name: 'TTSConfigs',
    component: TTSConfigs,
    meta: {
      title: 'TTS 语音合成配置',
    },
  },
  {
    path: '/agent-configs',
    name: 'AgentConfigs',
    component: AgentConfigs,
    meta: {
      title: 'Agent 智能体配置',
    },
  },
]


const router = createRouter({
  history: createWebHistory('/admin/'),
  routes,
})

export default router
