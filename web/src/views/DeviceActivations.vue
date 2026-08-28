<template>
  <div class="activations-container">
    <!-- 头部卡片：筛选与搜索 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="searchForm" class="search-form" @keyup.enter="handleSearch">
        <el-form-item label="设备序列号">
          <el-input
            v-model="searchForm.serial_number"
            placeholder="支持模糊搜索 SN"
            clearable
            style="width: 200px;"
          />
        </el-form-item>
        <el-form-item label="Device-Id">
          <el-input
            v-model="searchForm.device_id"
            placeholder="支持模糊搜索 Device-Id"
            clearable
            style="width: 200px;"
          />
        </el-form-item>
        <el-form-item label="Client-Id">
          <el-input
            v-model="searchForm.client_id"
            placeholder="支持搜索 Client-Id"
            clearable
            style="width: 180px;"
          />
        </el-form-item>
        <el-form-item label="激活状态">
          <el-select
            v-model="searchForm.activation_status"
            placeholder="全部状态"
            clearable
            style="width: 150px;"
          >
            <el-option label="全部状态" value="" />
            <el-option label="正常激活 (active)" value="active" />
            <el-option label="已冻结 (frozen)" value="frozen" />
            <el-option label="已注销 (revoked)" value="revoked" />
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
          <el-button type="primary" :icon="Connection" @click="openBindDialog">
            激活设备
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

        <el-table-column prop="serial_number" label="设备序列号 (SN)" min-width="190">
          <template #default="{ row }">
            <div class="cell-flex">
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

        <el-table-column prop="device_id" label="设备 Device-Id" min-width="190">
          <template #default="{ row }">
            <div class="cell-flex">
              <span class="code-font">{{ row.device_id }}</span>
              <el-tooltip content="复制 Device-Id" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.device_id, 'Device-Id')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="client_id" label="客户端 Client-Id" min-width="160">
          <template #default="{ row }">
            <span v-if="row.client_id" class="code-font">{{ row.client_id }}</span>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>

        <el-table-column prop="activation_status" label="激活状态" width="130" align="center">
          <template #default="{ row }">
            <el-tag :type="getStatusTagType(row.activation_status)" size="small">
              {{ getStatusLabel(row.activation_status) }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="activated_at" label="首次激活时间" width="170" align="center">
          <template #default="{ row }">
            {{ formatTime(row.activated_at) }}
          </template>
        </el-table-column>

        <el-table-column prop="updated_at" label="更新时间" width="170" align="center">
          <template #default="{ row }">
            {{ formatTime(row.updated_at) }}
          </template>
        </el-table-column>

        <el-table-column label="操作" width="150" align="center" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" :icon="Edit" @click="openEditDialog(row)">
              编辑
            </el-button>
            <el-popconfirm
              title="确定要删除该设备激活记录吗？"
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

    <!-- 弹窗1：通过绑定接口激活设备 -->
    <el-dialog
      v-model="bindDialog.visible"
      title="激活设备"
      width="520px"
      :close-on-click-modal="false"
      @closed="handleBindDialogClosed"
    >
      <el-alert
        title="输入设备配网连接后屏幕显示的 6 位激活验证码（Code）完成设备激活与绑定。"
        type="info"
        :closable="false"
        show-icon
        style="margin-bottom: 18px;"
      />

      <el-form
        ref="bindFormRef"
        :model="bindDialog.form"
        :rules="bindRules"
        label-width="110px"
      >
        <el-form-item label="激活验证码" prop="code">
          <el-input
            v-model="bindDialog.form.code"
            placeholder="请输入设备屏幕显示的 6 位验证码"
            maxlength="6"
            clearable
          />
          <span class="form-item-tip">设备连网后屏幕上展示的 6 位数字激活码</span>
        </el-form-item>

        <el-form-item label="设备序列号" prop="sn">
          <el-input
            v-model="bindDialog.form.sn"
            placeholder="选填（带出厂 SN 设备无需填写）"
            clearable
          />
          <span class="form-item-tip">仅针对未烧录出厂 SN 的设备需要手动输入</span>
        </el-form-item>

        <el-form-item label="出厂 HMAC Key" prop="hmac">
          <el-input
            v-model="bindDialog.form.hmac"
            placeholder="选填（无 SN 设备需输入 64 位 Key）"
            clearable
          />
          <span class="form-item-tip">仅无出厂 SN 设备在手动激活绑定时校验</span>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="bindDialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="bindDialog.loading" @click="submitBind">
          确认激活
        </el-button>
      </template>
    </el-dialog>

    <!-- 弹窗2：编辑设备激活记录 -->
    <el-dialog
      v-model="editDialog.visible"
      title="编辑设备激活记录"
      width="500px"
      :close-on-click-modal="false"
    >
      <el-form
        ref="editFormRef"
        :model="editDialog.form"
        label-width="110px"
      >
        <el-form-item label="设备序列号">
          <el-input :model-value="editDialog.form.serial_number" disabled />
        </el-form-item>

        <el-form-item label="Device-Id">
          <el-input
            v-model="editDialog.form.device_id"
            placeholder="如 MAC 地址或设备 Id"
            clearable
          />
        </el-form-item>

        <el-form-item label="Client-Id">
          <el-input
            v-model="editDialog.form.client_id"
            placeholder="如客户端实例标识"
            clearable
          />
        </el-form-item>

        <el-form-item label="激活状态">
          <el-select v-model="editDialog.form.activation_status" style="width: 100%;">
            <el-option label="正常激活 (active)" value="active" />
            <el-option label="已冻结 (frozen)" value="frozen" />
            <el-option label="已注销 (revoked)" value="revoked" />
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
  Connection,
  Delete,
  Refresh,
  CopyDocument,
  Edit,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  fetchActivations,
  bindDevice,
  updateActivation,
  deleteActivation,
  batchDeleteActivations,
  type ActivationItem,
} from '../api/deviceActivation'

// 列表查询条件
const searchForm = reactive({
  serial_number: '',
  device_id: '',
  client_id: '',
  activation_status: '',
})

// 表格数据与分页
const loading = ref(false)
const tableData = ref<ActivationItem[]>([])
const selectedRows = ref<ActivationItem[]>([])

const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 激活设备（绑定）弹窗状态
const bindFormRef = ref<FormInstance>()
const bindDialog = reactive({
  visible: false,
  loading: false,
  form: {
    code: '',
    sn: '',
    hmac: '',
  },
})

const bindRules: FormRules = {
  code: [
    { required: true, message: '请输入 6 位激活验证码', trigger: 'blur' },
    { min: 6, max: 6, message: '验证码长度必须为 6 位', trigger: 'blur' },
  ],
}

// 编辑弹窗状态
const editFormRef = ref<FormInstance>()
const editDialog = reactive({
  visible: false,
  loading: false,
  id: 0,
  form: {
    serial_number: '',
    device_id: '',
    client_id: '',
    activation_status: 'active',
  },
})

// 加载激活列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchActivations({
      page: pagination.page,
      page_size: pagination.pageSize,
      serial_number: searchForm.serial_number.trim() || undefined,
      device_id: searchForm.device_id.trim() || undefined,
      client_id: searchForm.client_id.trim() || undefined,
      activation_status: searchForm.activation_status || undefined,
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
  searchForm.device_id = ''
  searchForm.client_id = ''
  searchForm.activation_status = ''
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
function handleSelectionChange(rows: ActivationItem[]) {
  selectedRows.value = rows
}

// 打开激活设备弹窗
function openBindDialog() {
  bindDialog.form.code = ''
  bindDialog.form.sn = ''
  bindDialog.form.hmac = ''
  bindDialog.visible = true
}

function handleBindDialogClosed() {
  if (bindFormRef.value) {
    bindFormRef.value.resetFields()
  }
}

// 提交激活绑定
async function submitBind() {
  if (!bindFormRef.value) return
  await bindFormRef.value.validate(async (valid) => {
    if (!valid) return
    bindDialog.loading = true
    try {
      const res = await bindDevice({
        code: bindDialog.form.code.trim(),
        sn: bindDialog.form.sn.trim() || undefined,
        hmac: bindDialog.form.hmac.trim() || undefined,
      })
      if (res.success) {
        ElMessage.success(`设备激活成功！序列号: ${res.serial_number || '-'}, 设备 Id: ${res.device_id || '-'}`)
        bindDialog.visible = false
        loadData()
      }
    } catch (err: any) {
      ElMessage.error(`激活失败: ${err.message || err}`)
    } finally {
      bindDialog.loading = false
    }
  })
}

// 打开编辑弹窗
function openEditDialog(row: ActivationItem) {
  editDialog.id = row.id
  editDialog.form.serial_number = row.serial_number
  editDialog.form.device_id = row.device_id
  editDialog.form.client_id = row.client_id || ''
  editDialog.form.activation_status = row.activation_status
  editDialog.visible = true
}

// 提交编辑
async function submitEdit() {
  editDialog.loading = true
  try {
    const res = await updateActivation({
      id: editDialog.id,
      device_id: editDialog.form.device_id.trim(),
      client_id: editDialog.form.client_id.trim(),
      activation_status: editDialog.form.activation_status,
    })
    if (res.success) {
      ElMessage.success('更新设备激活记录成功')
      editDialog.visible = false
      loadData()
    }
  } catch (err: any) {
    ElMessage.error(`更新失败: ${err.message || err}`)
  } finally {
    editDialog.loading = false
  }
}

// 删除单条记录
async function handleDelete(row: ActivationItem) {
  try {
    const res = await deleteActivation(row.id)
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
      `确定要批量删除选中的 ${selectedRows.value.length} 条激活记录吗？该操作不可恢复。`,
      '批量删除确认',
      {
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        type: 'warning',
      }
    )

    const ids = selectedRows.value.map((r) => r.id)
    const res = await batchDeleteActivations(ids)
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

// 状态标签与显示
function getStatusTagType(status: string): '' | 'success' | 'warning' | 'info' | 'danger' {
  switch (status) {
    case 'active':
      return 'success'
    case 'frozen':
      return 'warning'
    case 'revoked':
      return 'danger'
    default:
      return 'info'
  }
}

function getStatusLabel(status: string): string {
  switch (status) {
    case 'active':
      return '正常激活'
    case 'frozen':
      return '已冻结'
    case 'revoked':
      return '已注销'
    default:
      return status || '-'
  }
}

onMounted(() => {
  loadData()
})
</script>

<style scoped>
.activations-container {
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
  gap: 12px;
}

.cell-flex {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.code-font {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, Courier, monospace;
  font-size: 13px;
  color: #303133;
}

.text-muted {
  color: #909399;
}

.copy-btn {
  padding: 2px 4px;
  height: auto;
}

.form-item-tip {
  display: block;
  font-size: 12px;
  color: #909399;
  line-height: 1.4;
  margin-top: 4px;
}

.pagination-wrapper {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}
</style>
