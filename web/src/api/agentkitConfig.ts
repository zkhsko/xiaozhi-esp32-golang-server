export interface AgentKitConfigItem {
  id: number
  tool_name: string
  tool_config: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  tool_name?: string
  enabled?: boolean | string
}

export interface ListResponseData {
  items: AgentKitConfigItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
}

export interface SaveAgentKitConfigParams {
  id?: number
  tool_name: string
  tool_config: string
  enabled?: boolean
}

const BASE_URL = '/admin-api'

export async function fetchAgentKitConfigs(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.tool_name) query.set('tool_name', params.tool_name)
  if (params.enabled !== undefined && params.enabled !== '') {
    query.set('enabled', String(params.enabled))
  }

  const res = await fetch(`${BASE_URL}/agentkit-config?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function saveAgentKitConfig(params: SaveAgentKitConfigParams): Promise<ApiResponse<AgentKitConfigItem>> {
  const res = await fetch(`${BASE_URL}/agentkit-config/save`, {
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

export async function deleteAgentKitConfig(id: number): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/agentkit-config/delete`, {
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

export async function batchDeleteAgentKitConfigs(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/agentkit-config/batch-delete`, {
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
