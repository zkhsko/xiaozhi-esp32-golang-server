export interface ActivationItem {
  id: number
  serial_number: string
  device_id: string
  client_id?: string
  activation_status: string
  activated_at: string
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  serial_number?: string
  device_id?: string
  client_id?: string
  activation_status?: string
}

export interface ListResponseData {
  items: ActivationItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
}

export interface BindDeviceParams {
  code: string
  sn?: string
  hmac?: string
}

export interface BindDeviceResponse {
  success: boolean
  message?: string
  serial_number?: string
  device_id?: string
  client_id?: string
  user_id?: number
}

export interface UpdateActivationParams {
  id: number
  device_id?: string
  client_id?: string
  activation_status?: string
}

const ADMIN_BASE_URL = '/admin-api'
const USER_BASE_URL = '/user-api'

export async function fetchActivations(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.serial_number) query.set('serial_number', params.serial_number)
  if (params.device_id) query.set('device_id', params.device_id)
  if (params.client_id) query.set('client_id', params.client_id)
  if (params.activation_status) query.set('activation_status', params.activation_status)

  const res = await fetch(`${ADMIN_BASE_URL}/device-activation?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function bindDevice(params: BindDeviceParams): Promise<BindDeviceResponse> {
  const res = await fetch(`${USER_BASE_URL}/device/bind`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      code: params.code.trim(),
      sn: params.sn?.trim() || undefined,
      hmac: params.hmac?.trim() || undefined,
    }),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    const text = await res.text()
    let msg = text
    try {
      const parsed = JSON.parse(text)
      if (parsed.message) msg = parsed.message
    } catch {}
    throw new Error(msg || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function updateActivation(params: UpdateActivationParams): Promise<ApiResponse> {
  const res = await fetch(`${ADMIN_BASE_URL}/device-activation/update`, {
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

export async function deleteActivation(id: number): Promise<ApiResponse> {
  const res = await fetch(`${ADMIN_BASE_URL}/device-activation/delete`, {
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

export async function batchDeleteActivations(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${ADMIN_BASE_URL}/device-activation/batch-delete`, {
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
