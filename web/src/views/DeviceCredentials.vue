<template>
  <div class="credentials-container">
    <!-- 头部卡片：筛选与搜索 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="searchForm" class="search-form" @keyup.enter="handleSearch">
        <el-form-item label="设备序列号">
          <el-input
            v-model="searchForm.serial_number"
            placeholder="支持模糊搜索 SN"
            clearable
            style="width: 220px;"
          />
        </el-form-item>
        <el-form-item label="设备类型">
          <el-input
            v-model="searchForm.device_type"
            placeholder="如 default"
            clearable
            style="width: 160px;"
          />
        </el-form-item>
        <el-form-item label="凭证状态">
          <el-select
            v-model="searchForm.credential_status"
            placeholder="全部状态"
            clearable
            style="width: 160px;"
          >
            <el-option label="全部状态" value="" />
            <el-option label="待激活 (enabled)" value="enabled" />
            <el-option label="已激活 (activated)" value="activated" />
            <el-option label="已禁用 (blocked)" value="blocked" />
            <el-option label="已作废 (revoked)" value="revoked" />
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
          <el-button type="primary" :icon="Plus" @click="openGenerateDialog">
            批量生成凭证
          </el-button>
          <el-button
            type="danger"
            :icon="Delete"
            :disabled="selectedRows.length === 0"
            @click="handleBatchDelete"
          >
            批量删除 ({{ selectedRows.length }})
          </el-button>
          <el-dropdown @command="handleExportCommand">
            <el-button :icon="Download">
              导出 CSV 数据 <el-icon class="el-icon--right"><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="selected" :disabled="selectedRows.length === 0">
                  导出已选记录 ({{ selectedRows.length }} 条)
                </el-dropdown-item>
                <el-dropdown-item command="current">
                  导出当前页 ({{ tableData.length }} 条)
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
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
        <el-table-column prop="id" label="ID" width="75" align="center" />

        <el-table-column prop="serial_number" label="设备序列号 (SN)" min-width="200">
          <template #default="{ row }">
            <div class="sn-cell">
              <span class="code-font">{{ row.serial_number }}</span>
              <el-tooltip content="复制序列号" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.serial_number, '序列号')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="hmac_key" label="出厂 HMAC Key" min-width="260">
          <template #default="{ row }">
            <div class="key-cell">
              <span class="code-font">
                {{ showPlainKey[row.id] ? row.hmac_key : maskKey(row.hmac_key) }}
              </span>
              <div class="key-actions">
                <el-tooltip :content="showPlainKey[row.id] ? '隐藏明文' : '查看明文'" placement="top">
                  <el-button
                    link
                    type="primary"
                    :icon="showPlainKey[row.id] ? View : Hide"
                    @click="toggleKeyVisibility(row.id)"
                  />
                </el-tooltip>
                <el-tooltip content="复制 HMAC Key" placement="top">
                  <el-button
                    link
                    type="primary"
                    :icon="CopyDocument"
                    @click="copyText(row.hmac_key, 'HMAC Key')"
                  />
                </el-tooltip>
              </div>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="device_type" label="设备类型" width="130" align="center">
          <template #default="{ row }">
            <el-tag effect="light" size="small">{{ row.device_type || 'default' }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="auth_method" label="认证方式" width="140" align="center">
          <template #default="{ row }">
            <el-tag type="info" effect="plain" size="small">{{ row.auth_method }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="credential_status" label="凭证状态" width="130" align="center">
          <template #default="{ row }">
            <el-tag :type="getStatusTagType(row.credential_status)" size="small">
              {{ getStatusLabel(row.credential_status) }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="created_at" label="创建时间" width="170" align="center">
          <template #default="{ row }">
            {{ formatTime(row.created_at) }}
          </template>
        </el-table-column>

        <el-table-column label="操作" width="140" align="center" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" :icon="Edit" @click="openEditDialog(row)">
              编辑
            </el-button>
            <el-popconfirm
              title="确定要删除该设备出厂凭证吗？"
              confirm-button-text="删除"
              cancel-button-text="取消"
              confirm-button-type="danger"
              @confirm="handleDelete(row)"
            >
              <template #reference>
                <el-button link type="danger" size="small" :icon="Delete">
                  删除
                </el-button>
              </template>
            </el-popconfirm>
          </template>
        </el-table-column>
      </el-table>

      <!-- 底部分页 -->
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

    <!-- 弹窗1：批量生成设备凭证 -->
    <el-dialog
      v-model="generateDialog.visible"
      title="批量生成设备出厂凭证"
      width="680px"
      :close-on-click-modal="false"
      @closed="handleGenerateDialogClosed"
    >
      <div v-if="!generateDialog.generatedItems.length">
        <el-form
          ref="generateFormRef"
          :model="generateDialog.form"
          :rules="generateRules"
          label-width="100px"
        >
          <el-form-item label="生成数量" prop="count">
            <el-input-number
              v-model="generateDialog.form.count"
              :min="1"
              :max="1000"
              :step="1"
              controls-position="right"
              style="width: 200px;"
            />
            <span class="form-item-tip">单次最多可生成 1000 个凭证</span>
          </el-form-item>

          <el-form-item label="设备类型" prop="device_type">
            <el-input
              v-model="generateDialog.form.device_type"
              placeholder="默认 default"
              style="width: 260px;"
            />
            <span class="form-item-tip">用于关联对应的 AI Agent 配置</span>
          </el-form-item>
        </el-form>
      </div>

      <!-- 生成成功后直接展示结果 -->
      <div v-else class="generate-result">
        <el-alert
          :title="`成功生成 ${generateDialog.generatedItems.length} 个设备出厂凭证！`"
          type="success"
          :closable="false"
          show-icon
          style="margin-bottom: 16px;"
        />
        <el-table
          :data="generateDialog.generatedItems"
          max-height="300"
          border
          stripe
          size="small"
        >
          <el-table-column prop="serial_number" label="序列号 (SN)" min-width="180">
            <template #default="{ row }">
              <span class="code-font">{{ row.serial_number }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="hmac_key" label="出厂 HMAC Key" min-width="260">
            <template #default="{ row }">
              <span class="code-font">{{ row.hmac_key }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="device_type" label="设备类型" width="100" />
        </el-table>
      </div>

      <template #footer>
        <div v-if="!generateDialog.generatedItems.length">
          <el-button @click="generateDialog.visible = false">取消</el-button>
          <el-button type="primary" :loading="generateDialog.loading" @click="submitGenerate">
            立即生成
          </el-button>
        </div>
        <div v-else style="display: flex; justify-content: space-between; align-items: center;">
          <div>
            <el-button type="primary" plain :icon="Download" @click="exportGeneratedCSV">
              下载 CSV 文件
            </el-button>
            <el-button :icon="CopyDocument" @click="copyGeneratedText">
              复制全部内容
            </el-button>
          </div>
          <el-button type="primary" @click="generateDialog.visible = false">
            完成
          </el-button>
        </div>
      </template>
    </el-dialog>

    <!-- 弹窗2：编辑设备凭据 -->
    <el-dialog
      v-model="editDialog.visible"
      title="编辑设备出厂凭证"
      width="480px"
      :close-on-click-modal="false"
    >
      <el-form
        ref="editFormRef"
        :model="editDialog.form"
        label-width="100px"
      >
        <el-form-item label="设备序列号">
          <el-input :model-value="editDialog.form.serial_number" disabled />
        </el-form-item>

        <el-form-item label="设备类型">
          <el-input
            v-model="editDialog.form.device_type"
            placeholder="如 default"
            clearable
          />
        </el-form-item>

        <el-form-item label="凭证状态">
          <el-select v-model="editDialog.form.credential_status" style="width: 100%;">
            <el-option label="待激活 (enabled)" value="enabled" />
            <el-option label="已激活 (activated)" value="activated" />
            <el-option label="已禁用 (blocked)" value="blocked" />
            <el-option label="已作废 (revoked)" value="revoked" />
          </el-select>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="editDialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="editDialog.loading" @click="submitEdit">
          保存修改
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
  Download,
  Refresh,
  CopyDocument,
  View,
  Hide,
  Edit,
  ArrowDown,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  fetchCredentials,
  generateCredentials,
  updateCredential,
  deleteCredential,
  batchDeleteCredentials,
  type CredentialItem,
} from '../api/deviceCredential'

// 列表查询条件
const searchForm = reactive({
  serial_number: '',
  device_type: '',
  credential_status: '',
})

// 表格数据与分页
const loading = ref(false)
const tableData = ref<CredentialItem[]>([])
const selectedRows = ref<CredentialItem[]>([])
const showPlainKey = reactive<Record<number, boolean>>({})

const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 批量生成弹窗状态
const generateDialog = reactive({
  visible: false,
  loading: false,
  form: {
    count: 10,
    device_type: 'default',
  },
  generatedItems: [] as CredentialItem[],
})

const generateRules = {
  count: [{ required: true, message: '请输入生成数量', trigger: 'blur' }],
}

// 编辑弹窗状态
const editDialog = reactive({
  visible: false,
  loading: false,
  id: 0,
  form: {
    serial_number: '',
    device_type: 'default',
    credential_status: 'enabled',
  },
})

// 加载凭证列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchCredentials({
      page: pagination.page,
      page_size: pagination.pageSize,
      serial_number: searchForm.serial_number.trim() || undefined,
      device_type: searchForm.device_type.trim() || undefined,
      credential_status: searchForm.credential_status || undefined,
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
  searchForm.serial_number = ''
  searchForm.device_type = ''
  searchForm.credential_status = ''
  pagination.page = 1
  loadData()
}

// 分页变化
function handlePageChange(newPage: number) {
  pagination.page = newPage
  loadData()
}

function handleSizeChange(newSize: number) {
  pagination.pageSize = newSize
  pagination.page = 1
  loadData()
}

// 表格多选变化
function handleSelectionChange(rows: CredentialItem[]) {
  selectedRows.value = rows
}

// 掩码展示 HMAC Key
function maskKey(key: string): string {
  if (!key) return ''
  if (key.length <= 16) return key
  return `${key.slice(0, 8)}········${key.slice(-8)}`
}

// 切换 Key 明文/密文展示
function toggleKeyVisibility(id: number) {
  showPlainKey[id] = !showPlainKey[id]
}

// 复制文本到剪贴板
async function copyText(text: string, label: string) {
  if (!text) return
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
    } else {
      const textarea = document.createElement('textarea')
      textarea.value = text
      textarea.style.position = 'fixed'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      document.execCommand('copy')
      document.body.removeChild(textarea)
    }
    ElMessage.success(`${label} 已复制到剪贴板`)
  } catch (e) {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

// 格式化时间
function formatTime(timeStr: string): string {
  if (!timeStr) return '-'
  try {
    const d = new Date(timeStr)
    if (isNaN(d.getTime())) return timeStr
    const YYYY = d.getFullYear()
    const MM = String(d.getMonth() + 1).padStart(2, '0')
    const DD = String(d.getDate()).padStart(2, '0')
    const hh = String(d.getHours()).padStart(2, '0')
    const mm = String(d.getMinutes()).padStart(2, '0')
    const ss = String(d.getSeconds()).padStart(2, '0')
    return `${YYYY}-${MM}-${DD} ${hh}:${mm}:${ss}`
  } catch {
    return timeStr
  }
}

// 状态标签
function getStatusTagType(status: string): '' | 'success' | 'warning' | 'info' | 'danger' {
  switch (status) {
    case 'enabled':
      return 'success'
    case 'activated':
      return ''
    case 'blocked':
      return 'warning'
    case 'revoked':
      return 'info'
    default:
      return 'info'
  }
}

function getStatusLabel(status: string): string {
  switch (status) {
    case 'enabled':
      return '待激活'
    case 'activated':
      return '已激活'
    case 'blocked':
      return '已禁用'
    case 'revoked':
      return '已作废'
    default:
      return status || '未知'
  }
}

// 批量生成弹窗逻辑
function openGenerateDialog() {
  generateDialog.form.count = 10
  generateDialog.form.device_type = 'default'
  generateDialog.generatedItems = []
  generateDialog.visible = true
}

function handleGenerateDialogClosed() {
  if (generateDialog.generatedItems.length > 0) {
    loadData()
  }
  generateDialog.generatedItems = []
}

async function submitGenerate() {
  generateDialog.loading = true
  try {
    const res = await generateCredentials({
      count: generateDialog.form.count,
      device_type: generateDialog.form.device_type || 'default',
    })
    if (res.success) {
      generateDialog.generatedItems = res.items || (res.data ? [res.data] : [])
      ElMessage.success(`成功生成 ${generateDialog.generatedItems.length} 个设备凭证`)
    }
  } catch (err: any) {
    ElMessage.error(`生成失败: ${err.message || err}`)
  } finally {
    generateDialog.loading = false
  }
}

function exportGeneratedCSV() {
  exportCSV(generateDialog.generatedItems, `device_credentials_batch_${Date.now()}.csv`)
}

function copyGeneratedText() {
  const content = generateDialog.generatedItems
    .map(item => `${item.serial_number},${item.hmac_key},${item.device_type}`)
    .join('\n')
  copyText(content, '生成的凭证列表')
}

// 编辑逻辑
function openEditDialog(row: CredentialItem) {
  editDialog.id = row.id
  editDialog.form.serial_number = row.serial_number
  editDialog.form.device_type = row.device_type
  editDialog.form.credential_status = row.credential_status
  editDialog.visible = true
}

async function submitEdit() {
  editDialog.loading = true
  try {
    const res = await updateCredential({
      id: editDialog.id,
      device_type: editDialog.form.device_type.trim() || 'default',
      credential_status: editDialog.form.credential_status,
    })
    if (res.success) {
      ElMessage.success('更新成功')
      editDialog.visible = false
      loadData()
    }
  } catch (err: any) {
    ElMessage.error(`更新失败: ${err.message || err}`)
  } finally {
    editDialog.loading = false
  }
}

// 删除逻辑
async function handleDelete(row: CredentialItem) {
  try {
    const res = await deleteCredential(row.id)
    if (res.success) {
      ElMessage.success('删除成功')
      loadData()
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
      `确定要批量删除选中的 ${selectedRows.value.length} 条设备出厂凭证吗？`,
      '批量删除确认',
      {
        type: 'warning',
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger',
      }
    )
    const ids = selectedRows.value.map(row => row.id)
    const res = await batchDeleteCredentials(ids)
    if (res.success) {
      ElMessage.success('批量删除成功')
      loadData()
    }
  } catch (err: any) {
    if (err !== 'cancel') {
      ElMessage.error(`批量删除失败: ${err.message || err}`)
    }
  }
}

// 导出 CSV
function handleExportCommand(command: string) {
  if (command === 'selected') {
    if (selectedRows.value.length === 0) {
      ElMessage.warning('请先勾选需要导出的记录')
      return
    }
    exportCSV(selectedRows.value, `device_credentials_selected_${Date.now()}.csv`)
  } else if (command === 'current') {
    if (tableData.value.length === 0) {
      ElMessage.warning('当前页没有数据可导出')
      return
    }
    exportCSV(tableData.value, `device_credentials_page_${Date.now()}.csv`)
  }
}

function exportCSV(items: CredentialItem[], filename: string) {
  const header = ['ID', '序列号(SN)', 'HMAC Key', '设备类型', '认证方式', '状态', '创建时间']
  const rows = items.map(item => [
    item.id,
    item.serial_number,
    item.hmac_key,
    item.device_type,
    item.auth_method,
    item.credential_status,
    item.created_at,
  ])

  const csvContent = [
    header.join(','),
    ...rows.map(r => r.map(cell => `"${String(cell || '').replace(/"/g, '""')}"`).join(',')),
  ].join('\n')

  const blob = new Blob(['\uFEFF' + csvContent], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.setAttribute('href', url)
  link.setAttribute('download', filename)
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
  ElMessage.success(`已导出 ${items.length} 条记录`)
}

onMounted(() => {
  loadData()
})
</script>

<style scoped>
.credentials-container {
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
  align-items: center;
}

.toolbar-right {
  display: flex;
  align-items: center;
}

.sn-cell, .key-cell {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.code-font {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 13px;
  letter-spacing: 0.5px;
}

.copy-btn {
  margin-left: 6px;
  padding: 2px 4px;
}

.key-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: 8px;
}

.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

.form-item-tip {
  margin-left: 12px;
  font-size: 12px;
  color: #909399;
}

.generate-result {
  max-height: 400px;
  overflow-y: auto;
}
</style>
