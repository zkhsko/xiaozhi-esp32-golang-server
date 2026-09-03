<template>
  <div class="agent-configs-container">
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
            新建 Agent 配置
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
              <el-icon class="name-icon"><UserFilled /></el-icon>
              <span class="name-text">{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="asr_name" label="ASR 语音识别" min-width="150">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="plain" size="small" type="primary">
                {{ row.asr_name || `ASR #${row.asr_config_id}` }}
              </el-tag>
              <el-tooltip :content="`ASR 配置 Id: ${row.asr_config_id}`" placement="top">
                <span class="id-badge">#{{ row.asr_config_id }}</span>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="llm_name" label="LLM 语言模型" min-width="150">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="plain" size="small" type="success">
                {{ row.llm_name || `LLM #${row.llm_config_id}` }}
              </el-tag>
              <el-tooltip :content="`LLM 配置 Id: ${row.llm_config_id}`" placement="top">
                <span class="id-badge">#{{ row.llm_config_id }}</span>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="tts_name" label="TTS 语音合成" min-width="150">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="plain" size="small" type="warning">
                {{ row.tts_name || `TTS #${row.tts_config_id}` }}
              </el-tag>
              <el-tooltip :content="`TTS 配置 Id: ${row.tts_config_id}`" placement="top">
                <span class="id-badge">#{{ row.tts_config_id }}</span>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="voice" label="发音人 (Voice)" min-width="130">
          <template #default="{ row }">
            <div class="cell-flex">
              <el-tag effect="light" size="small" type="info">{{ row.voice }}</el-tag>
              <el-tooltip content="复制音色标识" placement="top">
                <el-button
                  link
                  type="primary"
                  :icon="CopyDocument"
                  class="copy-btn"
                  @click="copyText(row.voice, '音色标识')"
                />
              </el-tooltip>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="system_prompt" label="系统提示词" min-width="180">
          <template #default="{ row }">
            <div class="prompt-cell" v-if="row.system_prompt && row.system_prompt.trim()">
              <span class="prompt-preview">{{ getPromptPreview(row.system_prompt) }}</span>
              <el-button
                link
                type="primary"
                size="small"
                @click="openPromptDialog(row)"
              >
                查看全部 ({{ row.system_prompt.length }}字)
              </el-button>
            </div>
            <span v-else class="text-muted">未配置提示词</span>
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
                title="确定要删除该 Agent 配置吗？"
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

    <!-- 弹窗1：新建 / 编辑 Agent 配置 -->
    <el-dialog
      v-model="configDialog.visible"
      :title="configDialog.isEdit ? '编辑 Agent 智能体配置' : '新建 Agent 智能体配置'"
      width="640px"
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
        <el-form-item label="配置名称" prop="name">
          <el-input
            v-model="configDialog.form.name"
            maxlength="128"
            show-word-limit
            clearable
          />
        </el-form-item>

        <el-form-item label="ASR 语音识别" prop="asr_config_id">
          <el-select
            v-model="configDialog.form.asr_config_id"
            filterable
            style="width: 100%;"
          >
            <el-option
              v-for="item in asrOptions"
              :key="item.id"
              :label="`${item.name} (${item.model})`"
              :value="item.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="LLM 语言模型" prop="llm_config_id">
          <el-select
            v-model="configDialog.form.llm_config_id"
            filterable
            style="width: 100%;"
          >
            <el-option
              v-for="item in llmOptions"
              :key="item.id"
              :label="`${item.name} (${item.model})`"
              :value="item.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="TTS 语音合成" prop="tts_config_id">
          <el-select
            v-model="configDialog.form.tts_config_id"
            filterable
            style="width: 100%;"
            @change="handleTTSChange"
          >
            <el-option
              v-for="item in ttsOptions"
              :key="item.id"
              :label="`${item.name} (${item.model})`"
              :value="item.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="发音人音色" prop="voice">
          <el-input
            v-model="configDialog.form.voice"
            maxlength="128"
            show-word-limit
            clearable
          />
          <div v-if="suggestedVoices.length > 0" class="voice-suggestions">
            <span class="suggestion-label">推荐音色：</span>
            <el-tag
              v-for="v in suggestedVoices"
              :key="v"
              size="small"
              class="suggestion-tag"
              @click="configDialog.form.voice = v"
            >
              {{ v }}
            </el-tag>
          </div>
        </el-form-item>

        <el-form-item label="系统提示词" prop="system_prompt">
          <el-input
            v-model="configDialog.form.system_prompt"
            type="textarea"
            :rows="6"
            maxlength="16384"
            show-word-limit
          />
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

    <!-- 弹窗2：查看完整系统提示词 -->
    <el-dialog
      v-model="promptDialog.visible"
      title="Agent 系统提示词详情"
      width="600px"
    >
      <div class="prompt-viewer">
        <div class="viewer-header">
          <span class="viewer-title">所属 Agent：{{ promptDialog.configName }}</span>
          <span class="viewer-stat">共 {{ promptDialog.content.length }} 个字符</span>
        </div>
        <el-input
          v-model="promptDialog.content"
          type="textarea"
          :rows="12"
          readonly
          class="prompt-textarea"
        />
      </div>
      <template #footer>
        <el-button :icon="CopyDocument" @click="copyText(promptDialog.content, '系统提示词')">
          复制提示词
        </el-button>
        <el-button type="primary" @click="promptDialog.visible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import {
  Search,
  RefreshRight,
  Plus,
  Delete,
  Refresh,
  CopyDocument,
  Edit,
  UserFilled,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import {
  fetchAgentConfigs,
  saveAgentConfig,
  deleteAgentConfig,
  batchDeleteAgentConfigs,
  type AgentConfigItem,
} from '../api/agentConfig'
import { fetchASRConfigs, type ASRConfigItem } from '../api/asrConfig'
import { fetchLLMConfigs, type LLMConfigItem } from '../api/llmConfig'
import { fetchTTSConfigs, type TTSConfigItem } from '../api/ttsConfig'

// 搜索表单
const searchForm = reactive({
  name: '',
  enabled: '',
})

// 表格数据与状态
const loading = ref(false)
const tableData = ref<(AgentConfigItem & { _switchLoading?: boolean })[]>([])
const selectedRows = ref<AgentConfigItem[]>([])

const pagination = reactive({
  page: 1,
  pageSize: 10,
  total: 0,
})

// 组件选项列表（供新建/编辑下拉框使用）
const asrOptions = ref<ASRConfigItem[]>([])
const llmOptions = ref<LLMConfigItem[]>([])
const ttsOptions = ref<TTSConfigItem[]>([])

// 新建/编辑弹窗
const configFormRef = ref<FormInstance>()
const configDialog = reactive({
  visible: false,
  isEdit: false,
  loading: false,
  form: {
    id: 0,
    name: '',
    asr_config_id: undefined as number | undefined,
    llm_config_id: undefined as number | undefined,
    tts_config_id: undefined as number | undefined,
    system_prompt: '',
    voice: '',
    enabled: true,
  },
})

// 表单校验规则
const configRules: FormRules = {
  name: [
    { required: true, message: '请输入配置名称', trigger: 'blur' },
    { max: 128, message: '配置名称不能超过 128 字符', trigger: 'blur' },
  ],
  asr_config_id: [
    { required: true, message: '请选择关联的 ASR 语音识别配置', trigger: 'change' },
  ],
  llm_config_id: [
    { required: true, message: '请选择关联的 LLM 语言模型配置', trigger: 'change' },
  ],
  tts_config_id: [
    { required: true, message: '请选择关联的 TTS 语音合成配置', trigger: 'change' },
  ],
  voice: [
    { required: true, message: '请输入发音人音色标识', trigger: 'blur' },
    { max: 128, message: '音色标识不能超过 128 字符', trigger: 'blur' },
  ],
  system_prompt: [
    { required: true, message: '请输入系统提示词', trigger: 'blur' },
    { max: 16384, message: '系统提示词不能超过 16384 字符', trigger: 'blur' },
  ],
}

// 推荐音色列表（根据当前选中的 TTS 配置解析）
const suggestedVoices = computed(() => {
  if (!configDialog.form.tts_config_id) return []
  const selectedTTS = ttsOptions.value.find((t) => t.id === configDialog.form.tts_config_id)
  if (!selectedTTS || !selectedTTS.voices) return []
  try {
    const parsed = JSON.parse(selectedTTS.voices)
    if (Array.isArray(parsed)) {
      return parsed.map((item: any) => (typeof item === 'string' ? item : item.name || item.id || String(item)))
    }
  } catch {
    // voices 非 JSON 数组时按英文逗号分隔
    return selectedTTS.voices.split(',').map((s) => s.trim()).filter(Boolean)
  }
  return []
})

function handleTTSChange(newTTSId: number) {
  // 若当前音色为空，且选中的 TTS 有推荐音色，自动填入首个音色
  const selectedTTS = ttsOptions.value.find((t) => t.id === newTTSId)
  if (selectedTTS && selectedTTS.voices && !configDialog.form.voice) {
    try {
      const parsed = JSON.parse(selectedTTS.voices)
      if (Array.isArray(parsed) && parsed.length > 0) {
        configDialog.form.voice = typeof parsed[0] === 'string' ? parsed[0] : parsed[0].name || parsed[0].id || ''
      }
    } catch {
      // ignore
    }
  }
}

// 提示词查看弹窗
const promptDialog = reactive({
  visible: false,
  configName: '',
  content: '',
})

// 截取提示词预览
function getPromptPreview(prompt: string): string {
  if (!prompt) return ''
  const oneLine = prompt.replace(/\r?\n|\r/g, ' ').trim()
  return oneLine.length > 25 ? oneLine.slice(0, 25) + '...' : oneLine
}

// 加载关联组件列表
async function loadComponentOptions() {
  try {
    const [asrRes, llmRes, ttsRes] = await Promise.all([
      fetchASRConfigs({ page: 1, page_size: 100 }),
      fetchLLMConfigs({ page: 1, page_size: 100 }),
      fetchTTSConfigs({ page: 1, page_size: 100 }),
    ])
    if (asrRes.success && asrRes.data) {
      asrOptions.value = asrRes.data.items || []
    }
    if (llmRes.success && llmRes.data) {
      llmOptions.value = llmRes.data.items || []
    }
    if (ttsRes.success && ttsRes.data) {
      ttsOptions.value = ttsRes.data.items || []
    }
  } catch (err) {
    console.error('加载关联组件列表失败', err)
  }
}

// 加载列表数据
async function loadData() {
  loading.value = true
  try {
    const res = await fetchAgentConfigs({
      page: pagination.page,
      page_size: pagination.pageSize,
      name: searchForm.name.trim() || undefined,
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
function handleSelectionChange(rows: AgentConfigItem[]) {
  selectedRows.value = rows
}

// 打开新建弹窗
function openCreateDialog() {
  loadComponentOptions()
  configDialog.isEdit = false
  configDialog.form = {
    id: 0,
    name: '',
    asr_config_id: asrOptions.value[0]?.id,
    llm_config_id: llmOptions.value[0]?.id,
    tts_config_id: ttsOptions.value[0]?.id,
    system_prompt: '',
    voice: '',
    enabled: true,
  }
  if (configDialog.form.tts_config_id) {
    handleTTSChange(configDialog.form.tts_config_id)
  }
  configDialog.visible = true
}

// 打开编辑弹窗
function openEditDialog(row: AgentConfigItem) {
  loadComponentOptions()
  configDialog.isEdit = true
  configDialog.form = {
    id: row.id,
    name: row.name,
    asr_config_id: row.asr_config_id,
    llm_config_id: row.llm_config_id,
    tts_config_id: row.tts_config_id,
    system_prompt: row.system_prompt,
    voice: row.voice,
    enabled: row.enabled,
  }
  configDialog.visible = true
}

// 打开系统提示词详情弹窗
function openPromptDialog(row: AgentConfigItem) {
  promptDialog.configName = row.name
  promptDialog.content = row.system_prompt || ''
  promptDialog.visible = true
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
        asr_config_id: configDialog.form.asr_config_id!,
        llm_config_id: configDialog.form.llm_config_id!,
        tts_config_id: configDialog.form.tts_config_id!,
        system_prompt: configDialog.form.system_prompt.trim(),
        voice: configDialog.form.voice.trim(),
        enabled: configDialog.form.enabled,
      }
      const res = await saveAgentConfig(payload)
      if (res.success) {
        ElMessage.success(configDialog.isEdit ? 'Agent 配置更新成功' : 'Agent 配置创建成功')
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
async function handleToggleEnabled(row: AgentConfigItem & { _switchLoading?: boolean }, targetVal: boolean) {
  row._switchLoading = true
  try {
    const res = await saveAgentConfig({
      id: row.id,
      name: row.name,
      asr_config_id: row.asr_config_id,
      llm_config_id: row.llm_config_id,
      tts_config_id: row.tts_config_id,
      system_prompt: row.system_prompt,
      voice: row.voice,
      enabled: targetVal,
    })
    if (res.success) {
      row.enabled = targetVal
      ElMessage.success(`已${targetVal ? '启用' : '禁用'}配置：${row.name}`)
    } else {
      ElMessage.error(res.message || '更新状态失败')
    }
  } catch (err: any) {
    ElMessage.error(`操作失败: ${err.message || err}`)
  } finally {
    row._switchLoading = false
  }
}

// 单条删除
async function handleDelete(row: AgentConfigItem) {
  try {
    const res = await deleteAgentConfig(row.id)
    if (res.success) {
      ElMessage.success('Agent 配置删除成功')
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
      `确定要批量删除选中的 ${selectedRows.value.length} 条 Agent 配置吗？`,
      '批量删除确认',
      {
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        type: 'warning',
      }
    )
    const ids = selectedRows.value.map((r) => r.id)
    const res = await batchDeleteAgentConfigs(ids)
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
  loadComponentOptions()
})
</script>

<style scoped>
.agent-configs-container {
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

.prompt-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.prompt-preview {
  font-size: 13px;
  color: #606266;
  line-height: 1.4;
  word-break: break-all;
}

.text-muted {
  color: #909399;
  font-size: 13px;
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

.voice-suggestions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 6px;
}

.suggestion-label {
  font-size: 12px;
  color: #909399;
}

.suggestion-tag {
  cursor: pointer;
  transition: all 0.2s;
}

.suggestion-tag:hover {
  background-color: #409eff;
  color: #ffffff;
}

.prompt-viewer {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.viewer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  background-color: #f5f7fa;
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 13px;
}

.viewer-title {
  font-weight: 500;
  color: #303133;
}

.viewer-stat {
  color: #909399;
}

.prompt-textarea :deep(.el-textarea__inner) {
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
}

.copy-btn {
  padding: 2px 4px;
  height: auto;
}
</style>
