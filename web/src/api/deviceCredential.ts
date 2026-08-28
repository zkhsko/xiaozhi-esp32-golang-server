export interface CredentialItem {
  id: number
  serial_number: string
  hmac_key: string
  auth_method: string
  device_type: string
  credential_status: string
  created_at: string
  updated_at: string
}

export interface ListQueryParams {
  page: number
  page_size: number
  serial_number?: string
  device_type?: string
  credential_status?: string
}

export interface ListResponseData {
  items: CredentialItem[]
  total: number
  page: number
  page_size: number
}

export interface ApiResponse<T = any> {
  success: boolean
  message?: string
  data?: T
  items?: CredentialItem[]
}

export interface GenerateParams {
  count: number
  device_type: string
}

export interface UpdateParams {
  id: number
  device_type?: string
  credential_status?: string
  auth_method?: string
}

const BASE_URL = '/admin-api'

export async function fetchCredentials(params: ListQueryParams): Promise<ApiResponse<ListResponseData>> {
  const query = new URLSearchParams()
  query.set('page', String(params.page))
  query.set('page_size', String(params.page_size))
  if (params.serial_number) query.set('serial_number', params.serial_number)
  if (params.device_type) query.set('device_type', params.device_type)
  if (params.credential_status) query.set('credential_status', params.credential_status)

  const res = await fetch(`${BASE_URL}/device-hmac-credential?${query.toString()}`, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function generateCredentials(params: GenerateParams): Promise<ApiResponse<CredentialItem>> {
  const res = await fetch(`${BASE_URL}/device-hmac-credential/generate`, {
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

export async function updateCredential(params: UpdateParams): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/device-hmac-credential/update`, {
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

export async function deleteCredential(id: number): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/device-hmac-credential/delete`, {
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

export async function batchDeleteCredentials(ids: number[]): Promise<ApiResponse> {
  const res = await fetch(`${BASE_URL}/device-hmac-credential/batch-delete`, {
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
