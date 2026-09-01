<template>
  <div class="agentkit-configs-container">
    <!-- 头部卡片：筛选与搜索 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="searchForm" class="search-form" @keyup.enter="handleSearch">
        <el-form-item label="工具标识">
          <el-input
            v-model="searchForm.tool_name"
            clearable
            style="width: 220px;"
          />
        </el-form-item>
        <el-form-item label="启用状态">
          <el-select
            v-model="searchForm.enabled"
            clearable
            style="width: 150px;"
          >
            <el-option label="全部状态" value="" />
            <el-option label="已启用" value="true" />
            <el-option label="已禁用" value="false" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :icon="Search" @click="handleSearch">查询</el-button>
          <el-button :icon="RefreshRight" @click="handleReset">重置</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 主体卡片：操作按钮与表格 -->
    <el-card class="table-card" shadow="never">
      <div class="table-toolbar">
        <div class="toolbar-left">
          <el-button type="primary" :icon="Plus" @click="openCreateDialog">
            新建内建工具配置
          </el-button>
          <el-button
            type="danger"
            :icon="Delete"
            :disabled="selectedRows.length === 0"
            @click="handleBatchDelete"
          >
            批量删除 ({{ selectedRows.length }})
          </el-button>
        </div>
        <div class="toolbar-right">
          <el-tooltip content="刷新数据" placement="top">
            <el-button :icon="Refresh" circle @click="loadData" />
          </el-tooltip>
        </div>
      </div>

      <!-- 数据表格 -->
      <el-table
        v-loading="loading"
        :data="tableData"
        row-key="id"
        border
        stripe
        @selection-change="handleSelectionChange"
        style="width: 100%;"
      >
        <el-table-column type="selection" width="50" align="center" />
        <el-table-column prop="id" label="Id" width="75" align="center" />

        <el-table-column prop="tool_name" label="工具标识 (Tool Name)" min-width="220">
          <template #default="{ row }">
            <div class="tool-name-cell">
              <el-icon class="tool-icon"><Tools /></el-icon>
              <span class="code-font strong-text">{{ row.tool_name }}</span>
              <el-tooltip content="复制工具标识" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.tool_name, '工具标识')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="tool_config" label="配置参数 (JSON)" min-width="260">
          <template #default="{ row }">
            <div class="config-cell">
              <span class="config-snippet code-font">{{ truncateConfig(row.tool_config) }}</span>
              <el-button
                link
                type="primary"
                size="small"
                @click="openDetailDialog(row)"
              >
                查看完整配置
              </el-button>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="enabled" label="状态" width="100" align="center">
          <template #default="{ row }">
            <el-tag
              :type="row.enabled ? 'success' : 'info'"
              effect="plain"
              size="small"
            >
              {{ row.enabled ? '已启用' : '已禁用' }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="created_at" label="创建时间" width="165" align="center">
          <template #default="{ row }">
            <span class="date-font">{{ formatDate(row.created_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="updated_at" label="更新时间" width="165" align="center">
          <template #default="{ row }">
            <span class="date-font">{{ formatDate(row.updated_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="160" align="center" fixed="right">
          <template #default="{ row }">
            <div class="action-buttons">
              <el-button
                link
                type="primary"
                :icon="Edit"
                @click="openEditDialog(row)"
              >
                编辑
              </el-button>
              <el-button
                link
                type="danger"
                :icon="Delete"
                @click="handleDelete(row)"
              >
                删除
              </el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>

      <!-- 分页区域 -->
      <div class="pagination-wrapper">
        <el-pagination
          v-model:current-page="pagination.page"
          v-model:page-size="pagination.pageSize"
          :page-sizes="[10, 20, 50, 100]"
          :total="pagination.total"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="handlePageChange"
          @size-change="handleSizeChange"
        />
      </div>
    </el-card>

    <!-- 新建/编辑弹窗 -->
    <el-dialog
      v-model="dialog.visible"
      :title="dialog.isEdit ? '编辑内建工具配置' : '新建内建工具配置'"
      width="680px"
      :close-on-click-modal="false"
      destroy-on-close
    >
      <el-form
        ref="formRef"
        :model="dialog.form"
        :rules="rules"
        label-width="110px"
        status-icon
      >
        <el-form-item label="工具标识" prop="tool_name">
          <el-input
            v-model="dialog.form.tool_name"
            clearable
          />
        </el-form-item>

        <el-form-item label="配置内容" prop="tool_config">
          <el-input
            v-model="dialog.form.tool_config"
            type="textarea"
            :rows="10"
            class="code-textarea"
          />
        </el-form-item>

        <el-form-item label="启用状态" prop="enabled">
          <el-switch
            v-model="dialog.form.enabled"
            active-text="启用"
            inactive-text="禁用"
          />
        </el-form-item>
      </el-form>

      <template #footer>
        <span class="dialog-footer">
          <el-button @click="dialog.visible = false">取消</el-button>
          <el-button
            type="primary"
            :loading="dialog.loading"
            @click="submitForm"
          >
            保存
          </el-button>
        </span>
      </template>
    </el-dialog>

    <!-- 查看完整配置详情弹窗 -->
    <el-dialog
      v-model="detailDialog.visible"
      title="工具配置参数详情"
      width="600px"
      destroy-on-close
    >
      <div class="detail-content">
        <div class="detail-row">
          <span class="detail-label">工具标识：</span>
          <span class="code-font strong-text">{{ detailDialog.tool_name }}</span>
        </div>
        <div class="detail-row">
          <span class="detail-label">启用状态：</span>
          <el-tag :type="detailDialog.enabled ? 'success' : 'info'" size="small">
            {{ detailDialog.enabled ? '已启用' : '已禁用' }}
          </el-tag>
        </div>
        <div class="detail-json-wrapper">
          <div class="detail-json-header">
            <span>JSON 配置参数</span>
            <el-button
              type="primary"
              link
              size="small"
              :icon="CopyDocument"
              @click="copyText(detailDialog.formatted_config, 'JSON 配置')"
            >
              复制内容
            </el-button>
          </div>
          <pre class="json-code-block">{{ detailDialog.formatted_config }}</pre>
        </div>
      </div>
      <template #footer>
        <el-button @click="detailDialog.visible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import {
  Search,
  RefreshRight,
  Plus,
  Delete,
  Refresh,
  Edit,
  Tools,
  CopyDocument,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  fetchAgentKitConfigs,
  saveAgentKitConfig,
  deleteAgentKitConfig,
  batchDeleteAgentKitConfigs,
  type AgentKitConfigItem,
} from '../api/agentkitConfig'

// 筛选表单
const searchForm = reactive({
  tool_name: '',
  enabled: '',
})

// 分页与表格状态
const loading = ref(false)
const tableData = ref<AgentKitConfigItem[]>([])
const selectedRows = ref<AgentKitConfigItem[]>([])
const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 新建/编辑弹窗状态
const formRef = ref<FormInstance>()
const dialog = reactive({
  visible: false,
  isEdit: false,
  loading: false,
  form: {
    id: 0,
    tool_name: '',
    tool_config: '',
    enabled: true,
  },
})

// 查看详情弹窗状态
const detailDialog = reactive({
  visible: false,
  tool_name: '',
  enabled: true,
  formatted_config: '',
})

// 自定义 JSON 校验规则
function validateJSONRule(_rule: any, value: string, callback: any) {
  if (!value || !value.trim()) {
    return callback(new Error('请输入工具配置内容'))
  }
  try {
    JSON.parse(value)
    callback()
  } catch {
    callback(new Error('配置内容必须是合法的 JSON 格式'))
  }
}

// 表单校验规则
const rules: FormRules = {
  tool_name: [
    { required: true, message: '请输入工具标识', trigger: 'blur' },
    { max: 128, message: '工具标识最大长度不能超过 128 个字符', trigger: 'blur' },
  ],
  tool_config: [
    { required: true, validator: validateJSONRule, trigger: 'blur' },
  ],
}

// 加载列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchAgentKitConfigs({
      page: pagination.page,
      page_size: pagination.pageSize,
      tool_name: searchForm.tool_name.trim() || undefined,
      enabled: searchForm.enabled !== '' ? searchForm.enabled : undefined,
    })
    if (res.success && res.data) {
      tableData.value = res.data.items || []
      pagination.total = res.data.total || 0
      pagination.page = res.data.page || 1
      pagination.pageSize = res.data.page_size || 10
    }
  } catch (err: any) {
    ElMessage.error(`加载数据失败: ${err.message || err}`)
  } finally {
    loading.value = false
  }
}

// 查询与重置
function handleSearch() {
  pagination.page = 1
  loadData()
}

function handleReset() {
  searchForm.tool_name = ''
  searchForm.enabled = ''
  pagination.page = 1
  loadData()
}

// 分页变更
function handlePageChange(newPage: number) {
  pagination.page = newPage
  loadData()
}

function handleSizeChange(newSize: number) {
  pagination.pageSize = newSize
  pagination.page = 1
  loadData()
}

// 表格多选
function handleSelectionChange(rows: AgentKitConfigItem[]) {
  selectedRows.value = rows
}

// 打开新建对话框
function openCreateDialog() {
  dialog.isEdit = false
  dialog.form = {
    id: 0,
    tool_name: '',
    tool_config: '',
    enabled: true,
  }
  dialog.visible = true
}

// 打开编辑对话框
function openEditDialog(row: AgentKitConfigItem) {
  dialog.isEdit = true
  dialog.form = {
    id: row.id,
    tool_name: row.tool_name,
    tool_config: row.tool_config,
    enabled: row.enabled,
  }
  dialog.visible = true
}

// 打开查看配置详情对话框
function openDetailDialog(row: AgentKitConfigItem) {
  detailDialog.tool_name = row.tool_name
  detailDialog.enabled = row.enabled
  try {
    detailDialog.formatted_config = JSON.stringify(JSON.parse(row.tool_config), null, 2)
  } catch {
    detailDialog.formatted_config = row.tool_config
  }
  detailDialog.visible = true
}

// 提交表单
async function submitForm() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    dialog.loading = true
    try {
      let normalizedJSON = dialog.form.tool_config.trim()
      try {
        normalizedJSON = JSON.stringify(JSON.parse(dialog.form.tool_config))
      } catch {
        // validate 阶段已拦截
      }

      const res = await saveAgentKitConfig({
        id: dialog.isEdit ? dialog.form.id : undefined,
        tool_name: dialog.form.tool_name.trim(),
        tool_config: normalizedJSON,
        enabled: dialog.form.enabled,
      })

      if (res.success) {
        ElMessage.success(dialog.isEdit ? '内建工具配置更新成功' : '内建工具配置创建成功')
        dialog.visible = false
        loadData()
      } else {
        ElMessage.error(res.message || '操作失败')
      }
    } catch (err: any) {
      ElMessage.error(`提交失败: ${err.message || err}`)
    } finally {
      dialog.loading = false
    }
  })
}

// 单条删除
async function handleDelete(row: AgentKitConfigItem) {
  try {
    await ElMessageBox.confirm(
      `确定要删除内建工具配置「${row.tool_name}」吗？删除后大模型将无法调用该工具。`,
      '删除确认',
      {
        type: 'warning',
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger',
      }
    )
    const res = await deleteAgentKitConfig(row.id)
    if (res.success) {
      ElMessage.success('删除成功')
      loadData()
    }
  } catch (action) {
    if (action !== 'cancel') {
      ElMessage.error('删除操作失败')
    }
  }
}

// 批量删除
async function handleBatchDelete() {
  if (selectedRows.value.length === 0) return
  try {
    await ElMessageBox.confirm(
      `确定要批量删除选中的 ${selectedRows.value.length} 项工具配置吗？此操作不可逆。`,
      '批量删除确认',
      {
        type: 'warning',
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger',
      }
    )
    const ids = selectedRows.value.map((r) => r.id)
    const res = await batchDeleteAgentKitConfigs(ids)
    if (res.success) {
      ElMessage.success(res.message || '批量删除成功')
      selectedRows.value = []
      loadData()
    }
  } catch (action) {
    if (action !== 'cancel') {
      ElMessage.error('批量删除操作失败')
    }
  }
}

// 截断展示配置字符串
function truncateConfig(cfg: string): string {
  if (!cfg) return '{}'
  if (cfg.length <= 60) return cfg
  return cfg.slice(0, 60) + '...'
}

// 格式化时间
function formatDate(dateStr: string): string {
  if (!dateStr) return '-'
  try {
    const d = new Date(dateStr)
    return d.toLocaleString('zh-CN', { hour12: false })
  } catch {
    return dateStr
  }
}

// 复制文本
async function copyText(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`${label}已复制到剪贴板`)
  } catch {
    ElMessage.error('复制失败，请手动选择复制')
  }
}

onMounted(() => {
  loadData()
})
</script>

<style scoped>
.agentkit-configs-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.filter-card {
  border-radius: 8px;
}

.search-form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: -18px;
}

.table-card {
  border-radius: 8px;
}

.table-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.toolbar-left {
  display: flex;
  gap: 10px;
}

.tool-name-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.tool-icon {
  font-size: 16px;
  color: #409eff;
}

.strong-text {
  font-weight: 600;
  color: #303133;
}

.code-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
}

.config-cell {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.config-snippet {
  color: #606266;
  background-color: #f5f7fa;
  padding: 2px 6px;
  border-radius: 4px;
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.date-font {
  font-size: 12px;
  color: #909399;
}

.copy-btn {
  padding: 2px;
  margin-left: 2px;
}

.action-buttons {
  display: flex;
  justify-content: center;
  gap: 4px;
}

.pagination-wrapper {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.code-textarea {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
}

.detail-content {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.detail-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
}

.detail-label {
  color: #606266;
  font-weight: 500;
}

.detail-json-wrapper {
  margin-top: 8px;
  border: 1px solid #dcdfe6;
  border-radius: 6px;
  overflow: hidden;
}

.detail-json-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 12px;
  background-color: #f5f7fa;
  border-bottom: 1px solid #dcdfe6;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.json-code-block {
  margin: 0;
  padding: 12px;
  background-color: #fafafa;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
  line-height: 1.5;
  color: #2c3e50;
  max-height: 360px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
