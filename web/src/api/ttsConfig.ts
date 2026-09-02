export interface TTSConfigItem {
  id: number
  name: string
  provider: string
  endpoint: string
  has_api_key: boolean
  model: string
  voices: string
  proxy_url: string
  connect_timeout_ms: number
  sentence_timeout_ms: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  name?: string
  provider?: string
  enabled?: boolean | string
}

export interface ListResponseData {
  items: TTSConfigItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
}

export interface SaveTTSConfigParams {
  id?: number
  name: string
  provider?: string
  endpoint: string
  api_key?: string
  model: string
  voices?: string
  proxy_url?: string
  connect_timeout_ms?: number
  sentence_timeout_ms?: number
  enabled?: boolean
}

const BASE_URL = '/admin-api'

export async function fetchTTSConfigs(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.name) query.set('name', params.name)
  if (params.provider) query.set('provider', params.provider)
  if (params.enabled !== undefined && params.enabled !== '') {
    query.set('enabled', String(params.enabled))
  }

  const res = await fetch(`${BASE_URL}/tts-config?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function saveTTSConfig(params: SaveTTSConfigParams): Promise<ApiResponse<TTSConfigItem>> {
  const res = await fetch(`${BASE_URL}/tts-config/save`, {
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

export async function deleteTTSConfig(id: number): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/tts-config/delete`, {
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

export async function batchDeleteTTSConfigs(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/tts-config/batch-delete`, {
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
