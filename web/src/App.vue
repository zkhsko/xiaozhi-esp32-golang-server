<template>
  <el-container class="layout-container">
    <el-aside width="220px" class="aside">
      <div class="logo">
        <el-icon class="logo-icon"><Cpu /></el-icon>
        <span class="logo-text">小智 ESP32 管理</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        class="el-menu-vertical"
        router
      >
        <el-menu-item index="/device-credentials">
          <el-icon><Key /></el-icon>
          <span>设备生产表</span>
        </el-menu-item>
        <el-menu-item index="/device-activations">
          <el-icon><Connection /></el-icon>
          <span>设备激活表</span>
        </el-menu-item>
        <el-menu-item index="/asr-configs">
          <el-icon><Microphone /></el-icon>
          <span>ASR 配置表</span>
        </el-menu-item>
        <el-menu-item index="/llm-configs">
          <el-icon><ChatDotRound /></el-icon>
          <span>LLM 配置表</span>
        </el-menu-item>
        <el-menu-item index="/tts-configs">
          <el-icon><Headset /></el-icon>
          <span>TTS 配置表</span>
        </el-menu-item>
        <el-menu-item index="/agent-configs">
          <el-icon><UserFilled /></el-icon>
          <span>Agent 配置表</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="header">
        <div class="header-left">
          <span class="header-title">{{ currentTitle }}</span>
        </div>
        <div class="header-right">
          <el-tag effect="plain" type="info">v1.0.0</el-tag>
        </div>
      </el-header>

      <el-main class="main-content">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { Cpu, Key, Connection, Microphone, ChatDotRound, Headset, UserFilled } from '@element-plus/icons-vue'

const route = useRoute()

const activeMenu = computed(() => {
  return route.path === '/' ? '/device-credentials' : route.path
})

const currentTitle = computed(() => {
  return (route.meta?.title as string) || '设备生产表管理'
})
</script>

<style>
html, body, #app {
  height: 100%;
  margin: 0;
  padding: 0;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
}

.layout-container {
  height: 100vh;
}

.aside {
  background-color: #ffffff;
  border-right: 1px solid #e4e7ed;
  display: flex;
  flex-direction: column;
}

.logo {
  height: 60px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 18px;
  border-bottom: 1px solid #e4e7ed;
  background-color: #ffffff;
}

.logo-icon {
  font-size: 22px;
  color: #409eff;
}

.logo-text {
  font-size: 16px;
  font-weight: 700;
  color: #303133;
}

.el-menu-vertical {
  border-right: none;
  background-color: transparent;
  padding-top: 8px;
}

.header {
  height: 60px;
  background-color: #ffffff;
  border-bottom: 1px solid #e4e7ed;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0 24px;
}

.header-title {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
}

.main-content {
  background-color: #f5f7fa;
  padding: 20px;
  overflow-y: auto;
}
</style>
