<template>
  <div class="device-types-container">
    <!-- 头部卡片：筛选与搜索 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="searchForm" class="search-form" @keyup.enter="handleSearch">
        <el-form-item label="设备类型">
          <el-input
            v-model="searchForm.device_type"
            clearable
            style="width: 200px;"
          />
        </el-form-item>
        <el-form-item label="关联智能体">
          <el-select
            v-model="searchForm.agent_config_id"
            clearable
            filterable
            style="width: 200px;"
          >
            <el-option label="全部智能体" value="" />
            <el-option
              v-for="agent in agentOptions"
              :key="agent.id"
              :label="`${agent.name} (#${agent.id})`"
              :value="agent.id"
            />
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
            新建设备类型
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

        <el-table-column prop="device_type" label="设备类型标识 (Device Type)" min-width="220">
          <template #default="{ row }">
            <div class="device-type-cell">
              <el-icon class="type-icon"><Cpu /></el-icon>
              <span class="code-font strong-text">{{ row.device_type }}</span>
              <el-tooltip content="复制设备类型" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.device_type, '设备类型')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="agent_name" label="关联智能体" min-width="220">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="plain" size="small" type="success">
                {{ row.agent_name || `智能体 #${row.agent_config_id}` }}
              </el-tag>
              <el-tooltip :content="`智能体 Id: ${row.agent_config_id}`" placement="top">
                <span class="id-badge">#{{ row.agent_config_id }}</span>
              </el-tooltip>
            </div>
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
                title="确定要删除该设备类型配置吗？"
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

    <!-- 弹窗：新建 / 编辑设备类型配置 -->
    <el-dialog
      v-model="configDialog.visible"
      :title="configDialog.isEdit ? '编辑设备类型' : '新建设备类型'"
      width="560px"
      :close-on-click-modal="false"
      destroy-on-close
    >
      <el-form
        ref="configFormRef"
        :model="configDialog.form"
        :rules="configRules"
        label-width="120px"
        label-position="right"
      >
        <el-form-item label="设备类型" prop="device_type">
          <el-input
            v-model="configDialog.form.device_type"
            maxlength="32"
            show-word-limit
            clearable
          />
          <span class="form-item-tip">设备类型唯一标识，通常由小写字母、数字和连字符组成（最大 32 字符）</span>
        </el-form-item>

        <el-form-item label="关联智能体" prop="agent_config_id">
          <el-select
            v-model="configDialog.form.agent_config_id"
            filterable
            style="width: 100%;"
          >
            <el-option
              v-for="item in agentOptions"
              :key="item.id"
              :label="`${item.name} (Id: ${item.id})`"
              :value="item.id"
            />
          </el-select>
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
  Cpu,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  fetchDeviceTypes,
  saveDeviceType,
  deleteDeviceType,
  batchDeleteDeviceTypes,
  type DeviceTypeItem,
} from '../api/deviceType'
import { fetchAgentConfigs, type AgentConfigItem } from '../api/agentConfig'

// 搜索表单
const searchForm = reactive({
  device_type: '',
  agent_config_id: '' as number | string,
})

// 表格数据与状态
const loading = ref(false)
const tableData = ref<DeviceTypeItem[]>([])
const selectedRows = ref<DeviceTypeItem[]>([])

const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 智能体选项列表
const agentOptions = ref<AgentConfigItem[]>([])

// 新建/编辑弹窗
const configFormRef = ref<FormInstance>()
const configDialog = reactive({
  visible: false,
  isEdit: false,
  loading: false,
  form: {
    id: 0,
    device_type: '',
    agent_config_id: undefined as number | undefined,
  },
})

// 表单校验规则
const configRules: FormRules = {
  device_type: [
    { required: true, message: '请输入设备类型标识', trigger: 'blur' },
    { max: 32, message: '设备类型标识不能超过 32 字符', trigger: 'blur' },
  ],
  agent_config_id: [
    { required: true, message: '请选择关联的智能体', trigger: 'change' },
  ],
}

// 加载智能体选项
async function loadAgentOptions() {
  try {
    const res = await fetchAgentConfigs({ page: 1, page_size: 100 })
    if (res.success && res.data) {
      agentOptions.value = res.data.items || []
    }
  } catch (err) {
    console.error('加载智能体列表失败', err)
  }
}

// 加载列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchDeviceTypes({
      page: pagination.page,
      page_size: pagination.pageSize,
      device_type: searchForm.device_type.trim() || undefined,
      agent_config_id: searchForm.agent_config_id !== '' ? searchForm.agent_config_id : undefined,
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

// 搜索与重置
function handleSearch() {
  pagination.page = 1
  loadData()
}

function handleReset() {
  searchForm.device_type = ''
  searchForm.agent_config_id = ''
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
function handleSelectionChange(rows: DeviceTypeItem[]) {
  selectedRows.value = rows
}

// 打开新建弹窗
function openCreateDialog() {
  loadAgentOptions()
  configDialog.isEdit = false
  configDialog.form = {
    id: 0,
    device_type: '',
    agent_config_id: agentOptions.value[0]?.id,
  }
  configDialog.visible = true
}

// 打开编辑弹窗
function openEditDialog(row: DeviceTypeItem) {
  loadAgentOptions()
  configDialog.isEdit = true
  configDialog.form = {
    id: row.id,
    device_type: row.device_type,
    agent_config_id: row.agent_config_id,
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
        device_type: configDialog.form.device_type.trim(),
        agent_config_id: configDialog.form.agent_config_id!,
      }
      const res = await saveDeviceType(payload)
      if (res.success) {
        ElMessage.success(configDialog.isEdit ? '设备类型配置更新成功' : '设备类型配置创建成功')
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

// 单条删除
async function handleDelete(row: DeviceTypeItem) {
  try {
    const res = await deleteDeviceType(row.id)
    if (res.success) {
      ElMessage.success('设备类型配置删除成功')
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
      `确定要批量删除选中的 ${selectedRows.value.length} 条设备类型配置吗？`,
      '批量删除确认',
      {
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        type: 'warning',
      }
    )
    const ids = selectedRows.value.map((r) => r.id)
    const res = await batchDeleteDeviceTypes(ids)
    if (res.success) {
      ElMessage.success(res.message || '批量删除成功')
      selectedRows.value = []
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

// 复制文本到剪贴板
async function copyText(text: string, label: string) {
  if (!text) {
    ElMessage.warning(`无可复制的${label}`)
    return
  }
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text)
      ElMessage.success(`已复制${label}到剪贴板`)
      return
    }
    throw new Error('Clipboard API不可用')
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
  loadAgentOptions()
})
</script>

<style scoped>
.device-types-container {
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

.device-type-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}

.type-icon {
  color: #409eff;
  font-size: 16px;
}

.strong-text {
  font-weight: 600;
  color: #303133;
}

.code-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
}

.cell-flex {
  display: flex;
  align-items: center;
  gap: 6px;
}

.id-badge {
  font-size: 11px;
  color: #909399;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
}

.date-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
  color: #606266;
}

.action-buttons {
  display: flex;
  justify-content: center;
  gap: 8px;
}

.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

.form-item-tip {
  display: block;
  font-size: 12px;
  color: #909399;
  line-height: 1.4;
  margin-top: 4px;
}

.copy-btn {
  padding: 2px 4px;
  height: auto;
}
</style>
