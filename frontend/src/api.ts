import type { APIError, Reservation, Stock } from './types'

const API_BASE = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    const apiError = data as APIError
    throw new Error(apiError.error?.message ?? `Request failed (${response.status})`)
  }
  return data as T
}
export const getStock = (itemId: string) =>
  request<Stock>(`/api/v1/inventory/stock?item_id=${encodeURIComponent(itemId)}`)
export type ReserveResponse = Omit<Reservation, 'status'> & {
  status: string
  reservation_status: Reservation['status']
}

export const reserveStock = (payload: {
  idempotency_key: string
  user_id: string
  item_id: string
  quantity: number
}) =>
  request<ReserveResponse>('/api/v1/inventory/reserve', {
    method: 'POST', body: JSON.stringify(payload),
  })
export const confirmReservation = (reservation_id: string) =>
  request<Reservation & { status: string }>('/api/v1/inventory/confirm', {
    method: 'POST', body: JSON.stringify({ reservation_id }),
  })
