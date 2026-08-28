export interface DeviceTypeItem {
  id: number
  device_type: string
  agent_config_id: number
  agent_name?: string
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  device_type?: string
  agent_config_id?: number | string
}

export interface ListResponseData {
  items: DeviceTypeItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
}

export interface SaveDeviceTypeParams {
  id?: number
  device_type: string
  agent_config_id: number
}

const BASE_URL = '/admin-api'

export async function fetchDeviceTypes(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.device_type) query.set('device_type', params.device_type)
  if (params.agent_config_id !== undefined && params.agent_config_id !== '') {
    query.set('agent_config_id', String(params.agent_config_id))
  }

  const res = await fetch(`${BASE_URL}/device-type?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function saveDeviceType(params: SaveDeviceTypeParams): Promise<ApiResponse<DeviceTypeItem>> {
  const res = await fetch(`${BASE_URL}/device-type/save`, {
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

export async function deleteDeviceType(id: number): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/device-type/delete`, {
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

export async function batchDeleteDeviceTypes(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/device-type/batch-delete`, {
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
