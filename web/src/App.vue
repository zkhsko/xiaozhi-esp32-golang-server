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
          <span>设备生产管理</span>
        </el-menu-item>
        <el-menu-item index="/device-activations">
          <el-icon><Connection /></el-icon>
          <span>设备激活管理</span>
        </el-menu-item>
        <el-menu-item index="/device-types">
          <el-icon><Cpu /></el-icon>
          <span>设备类型管理</span>
        </el-menu-item>
        <el-menu-item index="/asr-configs">
          <el-icon><Microphone /></el-icon>
          <span>语音识别配置管理</span>
        </el-menu-item>
        <el-menu-item index="/llm-configs">
          <el-icon><ChatDotRound /></el-icon>
          <span>大模型配置管理</span>
        </el-menu-item>
        <el-menu-item index="/tts-configs">
          <el-icon><Headset /></el-icon>
          <span>语音合成配置管理</span>
        </el-menu-item>
        <el-menu-item index="/agent-configs">
          <el-icon><UserFilled /></el-icon>
          <span>智能体配置管理</span>
        </el-menu-item>
        <el-menu-item index="/agentkit-configs">
          <el-icon><Tools /></el-icon>
          <span>内建工具管理</span>
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
import { Cpu, Key, Connection, Microphone, ChatDotRound, Headset, UserFilled, Tools } from '@element-plus/icons-vue'

const route = useRoute()

const activeMenu = computed(() => {
  return route.path === '/' ? '/device-credentials' : route.path
})

const currentTitle = computed(() => {
  return (route.meta?.title as string) || '设备生产管理'
})
</script>

<style>
html, body, #app {
  height: 100%;
  margin: 0;
  padding: 0;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
  background-color: #f8fafc;
}

.layout-container {
  height: 100vh;
}

.aside {
  background-color: #ffffff;
  border-right: 1px solid #eef0f4;
  display: flex;
  flex-direction: column;
}

.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 18px;
  border-bottom: 1px solid #f1f5f9;
  background-color: #ffffff;
}

.logo-icon {
  font-size: 20px;
  color: #409eff;
}

.logo-text {
  font-size: 15px;
  font-weight: 650;
  color: #0f172a;
  letter-spacing: -0.2px;
}

.el-menu-vertical {
  border-right: none !important;
  background-color: transparent !important;
  padding: 6px 0;
}

.el-menu-vertical .el-menu-item {
  height: 40px;
  line-height: 40px;
  margin: 2px 8px;
  border-radius: 6px;
  font-size: 13.5px;
  color: #475569;
  transition: all 0.15s ease;
}

.el-menu-vertical .el-menu-item:hover {
  background-color: #f1f5f9;
  color: #0f172a;
}

.el-menu-vertical .el-menu-item.is-active {
  background-color: #ecf5ff;
  color: #409eff;
  font-weight: 600;
}

.header {
  height: 56px;
  background-color: #ffffff;
  border-bottom: 1px solid #eef0f4;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0 24px;
}

.header-title {
  font-size: 15px;
  font-weight: 650;
  color: #0f172a;
  letter-spacing: -0.2px;
}

.main-content {
  background-color: #f8fafc;
  padding: 20px 24px;
  overflow-y: auto;
}

/* 全局卡片细腻微调 */
.filter-card,
.table-card {
  border-radius: 8px !important;
  border: 1px solid #eef0f4 !important;
  box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.03) !important;
  background-color: #ffffff;
}

/* 表格全局质感优化 */
.el-table {
  --el-table-header-bg-color: #f8fafc !important;
  --el-table-header-text-color: #475569 !important;
  --el-table-border-color: #f1f5f9 !important;
  border-radius: 6px;
}

.el-table th.el-table__cell {
  font-weight: 600 !important;
  font-size: 13px !important;
  height: 42px;
  background-color: #f8fafc !important;
}

.el-table td.el-table__cell {
  font-size: 13px;
  color: #334155;
  padding: 10px 0;
}

.el-table--striped .el-table__body tr.el-table__row--striped td.el-table__cell {
  background: #fafbfe;
}

/* 弹窗防溢出与圆角规范 */
.el-dialog {
  border-radius: 10px !important;
  overflow: hidden;
  display: flex !important;
  flex-direction: column !important;
  max-height: 85vh !important;
  margin-top: 7.5vh !important;
}

.el-dialog__header {
  padding: 16px 20px !important;
  margin-right: 0 !important;
  border-bottom: 1px solid #f1f5f9;
}

.el-dialog__title {
  font-size: 15px !important;
  font-weight: 650 !important;
  color: #0f172a !important;
}

.el-dialog__body {
  flex: 1 !important;
  overflow-y: auto !important;
  padding: 20px 24px !important;
}

.el-dialog__footer {
  padding: 12px 20px !important;
  border-top: 1px solid #f1f5f9;
  background-color: #fafbfc;
}
</style>
