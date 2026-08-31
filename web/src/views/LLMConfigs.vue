<template>
  <div class="llm-configs-container">
    <!-- 头部卡片：筛选与搜索 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="searchForm" class="search-form" @keyup.enter="handleSearch">
        <el-form-item label="配置名称">
          <el-input
            v-model="searchForm.name"
            placeholder="支持模糊搜索配置名称"
            clearable
            style="width: 200px;"
          />
        </el-form-item>
        <el-form-item label="服务平台">
          <el-select
            v-model="searchForm.provider"
            clearable
            style="width: 160px;"
          >
            <el-option label="全部平台" value="" />
            <el-option label="阿里百炼" value="dashscope" />
            <el-option label="OpenAI" value="openai" />
            <el-option label="DeepSeek" value="deepseek" />
            <el-option label="火山引擎" value="volcengine" />
            <el-option label="Ollama" value="ollama" />
          </el-select>
        </el-form-item>
        <el-form-item label="启用状态">
          <el-select
            v-model="searchForm.enabled"
            placeholder="全部状态"
            clearable
            style="width: 140px;"
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
            新建 LLM 配置
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

        <el-table-column prop="name" label="配置名称" min-width="160">
          <template #default="{ row }">
            <div class="name-cell">
              <el-icon class="name-icon"><ChatDotRound /></el-icon>
              <span class="name-text">{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="provider" label="服务平台" width="130" align="center">
          <template #default="{ row }">
            <el-tag
              :type="getProviderTagType(row.provider)"
              effect="plain"
              size="small"
            >
              {{ row.provider || 'dashscope' }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="endpoint" label="服务端点 (Endpoint)" min-width="240">
          <template #default="{ row }">
            <div class="cell-flex">
              <span class="code-font">{{ row.endpoint }}</span>
              <el-tooltip content="复制服务端点" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.endpoint, '服务端点')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="proxy_url" label="代理地址 (Proxy)" min-width="180">
          <template #default="{ row }">
            <div class="cell-flex" v-if="row.proxy_url">
              <span class="code-font">{{ row.proxy_url }}</span>
              <el-tooltip content="复制代理地址" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.proxy_url, '代理地址')"
                />
              </el-tooltip>
            </div>
            <span v-else class="text-muted">未配置代理</span>
          </template>
        </el-table-column>

        <el-table-column prop="model" label="模型标识 (Model)" min-width="190">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="light" size="small" type="primary">{{ row.model }}</el-tag>
              <el-tooltip content="复制模型名称" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.model, '模型名称')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="has_api_key" label="API Key 状态" width="120" align="center">
          <template #default="{ row }">
            <el-tag :type="row.has_api_key ? 'success' : 'info'" effect="light" size="small">
              {{ row.has_api_key ? '已配置' : '未配置' }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="first_token_timeout_ms" label="首 Token 超时" width="130" align="center">
          <template #default="{ row }">
            <span>{{ row.first_token_timeout_ms }} ms</span>
          </template>
        </el-table-column>

        <el-table-column prop="overall_timeout_ms" label="总超时时间" width="120" align="center">
          <template #default="{ row }">
            <span>{{ row.overall_timeout_ms }} ms</span>
          </template>
        </el-table-column>

        <el-table-column prop="enabled" label="启用状态" width="100" align="center">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              :loading="row._switchLoading"
              @change="(val: string | number | boolean) => handleToggleEnabled(row, Boolean(val))"
            />
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

        <el-table-column label="操作" width="150" align="center" fixed="right">
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
              <el-popconfirm
                title="确定要删除该 LLM 配置吗？"
                confirm-button-text="确定删除"
                cancel-button-text="取消"
                confirm-button-type="danger"
                @confirm="handleDelete(row)"
              >
                <template #reference>
                  <el-button link type="danger" :icon="Delete">
                    删除
                  </el-button>
                </template>
              </el-popconfirm>
            </div>
          </template>
        </el-table-column>
      </el-table>

      <!-- 分页组件 -->
      <div class="pagination-wrapper">
        <el-pagination
          v-model:current-page="pagination.page"
          v-model:page-size="pagination.pageSize"
          :page-sizes="[10, 20, 50, 100]"
          :total="pagination.total"
          layout="total, sizes, prev, pager, next, jumper"
          @size-change="handleSizeChange"
          @current-change="handlePageChange"
        />
      </div>
    </el-card>

    <!-- 弹窗：新建 / 编辑 LLM 配置 -->
    <el-dialog
      v-model="configDialog.visible"
      :title="configDialog.isEdit ? '编辑 LLM 大语言模型配置' : '新建 LLM 大语言模型配置'"
      width="600px"
      :close-on-click-modal="false"
      destroy-on-close
    >
      <el-form
        ref="configFormRef"
        :model="configDialog.form"
        :rules="configRules"
        label-width="130px"
        label-position="right"
      >
        <el-form-item label="配置名称" prop="name">
          <el-input
            v-model="configDialog.form.name"
            maxlength="128"
            show-word-limit
            clearable
          />
          <span class="form-item-tip">用于在 Agent 配置中展示与标识此 LLM 配置</span>
        </el-form-item>

        <el-form-item label="服务平台" prop="provider">
          <el-select
            v-model="configDialog.form.provider"
            filterable
            allow-create
            default-first-option
            style="width: 100%;"
          >
            <el-option label="DashScope" value="dashscope" />
            <el-option label="OpenAI" value="openai" />
            <el-option label="DeepSeek" value="deepseek" />
            <el-option label="火山引擎" value="volcengine" />
            <el-option label="Ollama" value="ollama" />
          </el-select>
        </el-form-item>

        <el-form-item label="服务端点" prop="endpoint">
          <el-input
            v-model="configDialog.form.endpoint"
            clearable
          />
          <span class="form-item-tip">LLM HTTP 协议端点地址，必须以 http:// 或 https:// 开头</span>
        </el-form-item>

        <el-form-item label="代理地址" prop="proxy_url">
          <el-input
            v-model="configDialog.form.proxy_url"
            clearable
          />
          <span class="form-item-tip">代理服务器地址（支持 http://, https://, socks5://, socks5h://），非空即启用</span>
        </el-form-item>

        <el-form-item label="模型标识" prop="model">
          <el-input
            v-model="configDialog.form.model"
            maxlength="255"
            clearable
          />
          <span class="form-item-tip">大语言模型标识（如 qwen-max / gpt-4o / deepseek-chat）</span>
        </el-form-item>

        <el-form-item label="API Key" prop="api_key">
          <el-input
            v-model="configDialog.form.api_key"
            type="password"
            show-password
            clearable
          />
          <span class="form-item-tip">
            {{ configDialog.isEdit ? '编辑时留空将保留已有 Key，如需修改请输入新 Key' : '用于访问 LLM 服务的 API Key 或鉴权凭据' }}
          </span>
        </el-form-item>

        <el-form-item label="首 Token 超时" prop="first_token_timeout_ms">
          <el-input-number
            v-model="configDialog.form.first_token_timeout_ms"
            :min="3000"
            :max="30000"
            :step="500"
            style="width: 200px;"
          />
          <span style="margin-left: 8px; color: #909399;">毫秒 (ms)</span>
          <div class="form-item-tip">等待模型输出第一个 Token 的最大超时时间（3000 ~ 30000 毫秒）</div>
        </el-form-item>

        <el-form-item label="总超时时间" prop="overall_timeout_ms">
          <el-input-number
            v-model="configDialog.form.overall_timeout_ms"
            :min="10000"
            :max="180000"
            :step="1000"
            style="width: 200px;"
          />
          <span style="margin-left: 8px; color: #909399;">毫秒 (ms)</span>
          <div class="form-item-tip">大模型完整回复流式生成的最大超时时间（10000 ~ 180000 毫秒，且需大于首 Token 超时）</div>
        </el-form-item>

        <el-form-item label="启用状态" prop="enabled">
          <el-switch
            v-model="configDialog.form.enabled"
            active-text="启用"
            inactive-text="禁用"
          />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="configDialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="configDialog.loading" @click="submitConfig">
          {{ configDialog.isEdit ? '保存修改' : '立即创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import {
  Search,
  RefreshRight,
  Plus,
  Delete,
  Refresh,
  CopyDocument,
  Edit,
  ChatDotRound,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  fetchLLMConfigs,
  saveLLMConfig,
  deleteLLMConfig,
  batchDeleteLLMConfigs,
  type LLMConfigItem,
} from '../api/llmConfig'

// 搜索表单
const searchForm = reactive({
  name: '',
  provider: '',
  enabled: '',
})

// 表格数据与状态
const loading = ref(false)
const tableData = ref<(LLMConfigItem & { _switchLoading?: boolean })[]>([])
const selectedRows = ref<LLMConfigItem[]>([])

const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 新建/编辑弹窗
const configFormRef = ref<FormInstance>()
const configDialog = reactive({
  visible: false,
  isEdit: false,
  loading: false,
  form: {
    id: 0,
    name: '',
    provider: 'dashscope',
    endpoint: '',
    proxy_url: '',
    model: '',
    api_key: '',
    first_token_timeout_ms: 5000,
    overall_timeout_ms: 30000,
    enabled: true,
  },
})

// 表单校验规则
const validateEndpoint = (_rule: any, value: string, callback: any) => {
  if (!value || !value.trim()) {
    return callback(new Error('请输入服务端点 Endpoint'))
  }
  const trimmed = value.trim()
  if (!trimmed.startsWith('http://') && !trimmed.startsWith('https://')) {
    return callback(new Error('服务端点必须以 http:// 或 https:// 开头'))
  }
  callback()
}

// 校验代理地址格式
const validateProxyURL = (_rule: any, value: string, callback: any) => {
  if (!value || !value.trim()) {
    return callback()
  }
  const trimmed = value.trim()
  if (
    !trimmed.startsWith('http://') &&
    !trimmed.startsWith('https://') &&
    !trimmed.startsWith('socks5://') &&
    !trimmed.startsWith('socks5h://')
  ) {
    return callback(new Error('代理地址必须以 http://、https://、socks5:// 或 socks5h:// 开头'))
  }
  callback()
}

const validateOverallTimeout = (_rule: any, value: number, callback: any) => {
  if (!value) {
    return callback(new Error('请输入总超时时间'))
  }
  if (value < 10000 || value > 180000) {
    return callback(new Error('总超时时间必须在 10000 ~ 180000 毫秒之间'))
  }
  if (value <= configDialog.form.first_token_timeout_ms) {
    return callback(new Error('总超时时间必须大于首 Token 超时时间'))
  }
  callback()
}

const configRules: FormRules = {
  name: [
    { required: true, message: '请输入配置名称', trigger: 'blur' },
    { max: 128, message: '配置名称不能超过 128 字符', trigger: 'blur' },
  ],
  provider: [
    { required: true, message: '请选择或输入服务平台', trigger: 'change' },
    { max: 64, message: '服务平台标识不能超过 64 字符', trigger: 'blur' },
  ],
  endpoint: [
    { required: true, validator: validateEndpoint, trigger: 'blur' },
  ],
  proxy_url: [
    { validator: validateProxyURL, trigger: 'blur' },
  ],
  model: [
    { required: true, message: '请输入模型标识', trigger: 'blur' },
    { max: 255, message: '模型标识不能超过 255 字符', trigger: 'blur' },
  ],
  first_token_timeout_ms: [
    { required: true, message: '请输入首 Token 超时时间', trigger: 'blur' },
  ],
  overall_timeout_ms: [
    { required: true, validator: validateOverallTimeout, trigger: 'blur' },
  ],
}

// 平台标签颜色映射
function getProviderTagType(provider: string): '' | 'primary' | 'success' | 'warning' | 'info' | 'danger' {
  switch (provider) {
    case 'dashscope':
      return 'primary'
    case 'openai':
      return 'success'
    case 'deepseek':
      return 'warning'
    case 'volcengine':
      return 'info'
    default:
      return 'info'
  }
}

// 加载列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchLLMConfigs({
      page: pagination.page,
      page_size: pagination.pageSize,
      name: searchForm.name.trim() || undefined,
      provider: searchForm.provider.trim() || undefined,
      enabled: searchForm.enabled || undefined,
    })
    if (res.success && res.data) {
      tableData.value = (res.data.items || []).map((item) => ({
        ...item,
        _switchLoading: false,
      }))
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

// 搜索与重置
function handleSearch() {
  pagination.page = 1
  loadData()
}

function handleReset() {
  searchForm.name = ''
  searchForm.provider = ''
  searchForm.enabled = ''
  pagination.page = 1
  loadData()
}

// 分页控制
function handlePageChange(newPage: number) {
  pagination.page = newPage
  loadData()
}

function handleSizeChange(newSize: number) {
  pagination.pageSize = newSize
  pagination.page = 1
  loadData()
}

// 多选表格
function handleSelectionChange(rows: LLMConfigItem[]) {
  selectedRows.value = rows
}

// 打开新建弹窗
function openCreateDialog() {
  configDialog.isEdit = false
  configDialog.form = {
    id: 0,
    name: '',
    provider: 'dashscope',
    endpoint: '',
    proxy_url: '',
    model: '',
    api_key: '',
    first_token_timeout_ms: 5000,
    overall_timeout_ms: 30000,
    enabled: true,
  }
  configDialog.visible = true
}

// 打开编辑弹窗
function openEditDialog(row: LLMConfigItem) {
  configDialog.isEdit = true
  configDialog.form = {
    id: row.id,
    name: row.name,
    provider: row.provider || 'dashscope',
    endpoint: row.endpoint,
    proxy_url: row.proxy_url || '',
    model: row.model,
    api_key: '', // 编辑时默认留空
    first_token_timeout_ms: row.first_token_timeout_ms || 5000,
    overall_timeout_ms: row.overall_timeout_ms || 30000,
    enabled: row.enabled,
  }
  configDialog.visible = true
}

// 提交新建/编辑
async function submitConfig() {
  if (!configFormRef.value) return
  await configFormRef.value.validate(async (valid) => {
    if (!valid) return
    configDialog.loading = true
    try {
      const payload = {
        id: configDialog.isEdit ? configDialog.form.id : undefined,
        name: configDialog.form.name.trim(),
        provider: configDialog.form.provider.trim(),
        endpoint: configDialog.form.endpoint.trim(),
        proxy_url: configDialog.form.proxy_url.trim() || '',
        model: configDialog.form.model.trim(),
        api_key: configDialog.form.api_key.trim() || undefined,
        first_token_timeout_ms: configDialog.form.first_token_timeout_ms,
        overall_timeout_ms: configDialog.form.overall_timeout_ms,
        enabled: configDialog.form.enabled,
      }
      const res = await saveLLMConfig(payload)
      if (res.success) {
        ElMessage.success(configDialog.isEdit ? 'LLM 配置更新成功' : 'LLM 配置创建成功')
        configDialog.visible = false
        loadData()
      } else {
        ElMessage.error(res.message || '保存失败')
      }
    } catch (err: any) {
      ElMessage.error(`保存失败: ${err.message || err}`)
    } finally {
      configDialog.loading = false
    }
  })
}

// 快速切换启用状态
async function handleToggleEnabled(row: LLMConfigItem & { _switchLoading?: boolean }, targetVal: boolean) {
  row._switchLoading = true
  try {
    const res = await saveLLMConfig({
      id: row.id,
      name: row.name,
      provider: row.provider,
      endpoint: row.endpoint,
      proxy_url: row.proxy_url,
      model: row.model,
      first_token_timeout_ms: row.first_token_timeout_ms,
      overall_timeout_ms: row.overall_timeout_ms,
      enabled: targetVal,
    })
    if (res.success) {
      row.enabled = targetVal
      ElMessage.success(`已${targetVal ? '启用' : '禁用'}配置：${row.name}`)
    } else {
      ElMessage.error(res.message || '更新状态失败')
    }
  } catch (err: any) {
    ElMessage.error(`更新状态失败: ${err.message || err}`)
  } finally {
    row._switchLoading = false
  }
}

// 单条删除
async function handleDelete(row: LLMConfigItem) {
  try {
    const res = await deleteLLMConfig(row.id)
    if (res.success) {
      ElMessage.success('LLM 配置删除成功')
      loadData()
    } else {
      ElMessage.error(res.message || '删除失败')
    }
  } catch (err: any) {
    ElMessage.error(`删除失败: ${err.message || err}`)
  }
}

// 批量删除
async function handleBatchDelete() {
  if (selectedRows.value.length === 0) return
  try {
    await ElMessageBox.confirm(
      `确定要批量删除选中的 ${selectedRows.value.length} 条 LLM 配置吗？`,
      '批量删除确认',
      {
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        type: 'warning',
      }
    )

    const ids = selectedRows.value.map((r) => r.id)
    const res = await batchDeleteLLMConfigs(ids)
    if (res.success) {
      ElMessage.success('批量删除成功')
      loadData()
    } else {
      ElMessage.error(res.message || '批量删除失败')
    }
  } catch (err: any) {
    if (err !== 'cancel') {
      ElMessage.error(`批量删除失败: ${err.message || err}`)
    }
  }
}

// 复制文本
async function copyText(text: string, label: string) {
  if (!text) {
    ElMessage.warning(`无可复制的${label}`)
    return
  }
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`已复制${label}到剪贴板`)
  } catch {
    const textarea = document.createElement('textarea')
    textarea.value = text
    document.body.appendChild(textarea)
    textarea.select()
    document.execCommand('copy')
    document.body.removeChild(textarea)
    ElMessage.success(`已复制${label}到剪贴板`)
  }
}

// 格式化日期
function formatDate(isoStr: string) {
  if (!isoStr) return '-'
  try {
    const d = new Date(isoStr)
    if (isNaN(d.getTime())) return isoStr
    const Y = d.getFullYear()
    const M = String(d.getMonth() + 1).padStart(2, '0')
    const D = String(d.getDate()).padStart(2, '0')
    const h = String(d.getHours()).padStart(2, '0')
    const m = String(d.getMinutes()).padStart(2, '0')
    const s = String(d.getSeconds()).padStart(2, '0')
    return `${Y}-${M}-${D} ${h}:${m}:${s}`
  } catch {
    return isoStr
  }
}

onMounted(() => {
  loadData()
})
</script>

<style scoped>
.llm-configs-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.filter-card {
  border-radius: 8px;
}

.search-form {
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

.name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 500;
}

.name-icon {
  color: #409eff;
  font-size: 16px;
}

.name-text {
  color: #303133;
}

.cell-flex {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 4px;
}

.code-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 12px;
  color: #409eff;
  background-color: #ecf5ff;
  padding: 2px 6px;
  border-radius: 4px;
  word-break: break-all;
}

.date-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 12px;
  color: #606266;
}

.copy-btn {
  padding: 2px;
  opacity: 0.6;
  transition: opacity 0.2s;
}

.copy-btn:hover {
  opacity: 1;
}

.action-buttons {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
}

.pagination-wrapper {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.form-item-tip {
  display: block;
  font-size: 12px;
  color: #909399;
  line-height: 1.4;
  margin-top: 4px;
}
</style>
