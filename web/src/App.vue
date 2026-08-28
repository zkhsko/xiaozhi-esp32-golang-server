<template>
  <el-container class="layout-container">
    <el-aside width="220px" class="aside">
      <div class="logo">
        <span class="logo-text">小智 ESP32 后台</span>
      </div>
      <el-menu
        default-active="/"
        class="el-menu-vertical"
        router
      >
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="header">
        <div class="header-left">
          <span class="header-title">管理控制台</span>
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
  background-color: #f8f9fa;
  border-right: 1px solid #e9ecef;
  display: flex;
  flex-direction: column;
}

.logo {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-bottom: 1px solid #e9ecef;
  background-color: #ffffff;
}

.logo-text {
  font-size: 16px;
  font-weight: 700;
  color: #303133;
}

.el-menu-vertical {
  border-right: none;
  background-color: transparent;
}

.header {
  height: 60px;
  background-color: #ffffff;
  border-bottom: 1px solid #e9ecef;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0 20px;
}

.header-title {
  font-size: 16px;
  font-weight: 500;
  color: #606266;
}

.main-content {
  background-color: #f0f2f5;
  padding: 20px;
}
</style>
