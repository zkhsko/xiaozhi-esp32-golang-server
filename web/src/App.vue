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
  background-color: var(--el-bg-color-page, #f2f3f5);
  color: var(--el-text-color-primary, #303133);
}

.layout-container {
  height: 100vh;
}

.aside {
  background-color: var(--el-bg-color, #ffffff);
  border-right: 1px solid var(--el-border-color-lighter, #ebeef5);
  display: flex;
  flex-direction: column;
}

.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 18px;
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
  background-color: var(--el-bg-color, #ffffff);
}

.logo-icon {
  font-size: 20px;
  color: var(--el-color-primary, #409eff);
}

.logo-text {
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
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
  border-radius: var(--el-border-radius-base, 4px);
  font-size: 13.5px;
  color: var(--el-text-color-regular, #606266);
  transition: all 0.15s ease;
}

.el-menu-vertical .el-menu-item:hover {
  background-color: var(--el-fill-color-light, #f5f7fa);
  color: var(--el-color-primary, #409eff);
}

.el-menu-vertical .el-menu-item.is-active {
  background-color: var(--el-color-primary-light-9, #ecf5ff);
  color: var(--el-color-primary, #409eff);
  font-weight: 600;
}

.header {
  height: 56px;
  background-color: var(--el-bg-color, #ffffff);
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0 24px;
}

.header-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
}

.main-content {
  background-color: var(--el-bg-color-page, #f2f3f5);
  padding: 20px 24px;
  overflow-y: auto;
}

/* 卡片规范：使用 Element Plus 默认边框与圆角规范 */
.filter-card,
.table-card {
  border-radius: var(--el-border-radius-base, 4px) !important;
  border: 1px solid var(--el-border-color-light, #e4e7ed) !important;
  background-color: var(--el-bg-color, #ffffff);
}

/* 表格全局质感优化：遵循 Element Plus 默认配色规范 */
.el-table {
  border-radius: var(--el-border-radius-base, 4px);
}

.el-table th.el-table__cell {
  font-weight: 600 !important;
  font-size: 13px !important;
  height: 42px;
  color: var(--el-text-color-primary, #303133) !important;
  background-color: var(--el-fill-color-light, #f5f7fa) !important;
}

.el-table td.el-table__cell {
  font-size: 13px;
  color: var(--el-text-color-regular, #606266);
  padding: 10px 0;
}

/* ==================== 统一弹窗展示规范 ==================== */
.el-dialog {
  display: flex !important;
  flex-direction: column !important;
  max-height: 85vh !important;
  margin: 0 auto !important;
  border-radius: var(--el-border-radius-base, 4px) !important;
  overflow: hidden !important;
  box-shadow: var(--el-box-shadow-light) !important;
}

.el-dialog__header {
  flex-shrink: 0 !important;
  padding: 16px 20px !important;
  margin-right: 0 !important;
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
}

.el-dialog__title {
  font-size: 16px !important;
  font-weight: 600 !important;
  color: var(--el-text-color-primary, #303133) !important;
  line-height: 24px !important;
}

.el-dialog__headerbtn {
  top: 16px !important;
  right: 18px !important;
  font-size: 16px !important;
}

.el-dialog__headerbtn .el-dialog__close {
  color: var(--el-text-color-secondary, #909399) !important;
}

.el-dialog__headerbtn:hover .el-dialog__close {
  color: var(--el-color-primary, #409eff) !important;
}

.el-dialog__body {
  flex: 1 1 auto !important;
  min-height: 0 !important; /* 关键：防止 flex 子项内容撑破容器导致无法滚动被截断 */
  overflow-y: auto !important;
  padding: 20px 24px !important;
  color: var(--el-text-color-regular, #606266) !important;
}

/* 优雅定制弹窗内部滚动条 */
.el-dialog__body::-webkit-scrollbar {
  width: 6px;
}

.el-dialog__body::-webkit-scrollbar-thumb {
  background-color: var(--el-border-color, #dcdfe6);
  border-radius: 3px;
}

.el-dialog__body::-webkit-scrollbar-thumb:hover {
  background-color: var(--el-text-color-secondary, #909399);
}

.el-dialog__body::-webkit-scrollbar-track {
  background-color: transparent;
}

.el-dialog__footer {
  flex-shrink: 0 !important;
  padding: 12px 20px !important;
  border-top: 1px solid var(--el-border-color-lighter, #ebeef5);
  background-color: var(--el-bg-color, #ffffff) !important;
  text-align: right;
}

/* 统一弹窗表单辅助提示文字规范 */
.form-item-tip {
  display: block;
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: var(--el-text-color-secondary, #909399);
}
</style>
