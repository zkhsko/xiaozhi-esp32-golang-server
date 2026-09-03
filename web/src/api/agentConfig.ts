export interface AgentConfigItem {
  id: number
  name: string
  asr_config_id: number
  asr_name?: string
  llm_config_id: number
  llm_name?: string
  tts_config_id: number
  tts_name?: string
  system_prompt: string
  voice: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  name?: string
  enabled?: boolean | string
}

export interface ListResponseData {
  items: AgentConfigItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
}

export interface SaveAgentConfigParams {
  id?: number
  name: string
  asr_config_id: number
  llm_config_id: number
  tts_config_id: number
  system_prompt: string
  voice: string
  enabled?: boolean
}

const BASE_URL = '/admin-api'

export async function fetchAgentConfigs(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.name) query.set('name', params.name)
  if (params.enabled !== undefined && params.enabled !== '') {
    query.set('enabled', String(params.enabled))
  }

  const res = await fetch(`${BASE_URL}/agent-config?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function saveAgentConfig(params: SaveAgentConfigParams): Promise<ApiResponse<AgentConfigItem>> {
  const res = await fetch(`${BASE_URL}/agent-config/save`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function deleteAgentConfig(id: number): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/agent-config/delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id }),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function batchDeleteAgentConfigs(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/agent-config/batch-delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}
