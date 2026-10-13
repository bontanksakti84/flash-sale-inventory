export type Stock = {
  item_id: string
  total_stock: number
  reserved_stock: number
  available_stock: number
}
export type Reservation = {
  status: 'active' | 'confirmed' | 'expired'
  reservation_id: string
  item_id: string
  quantity: number
  expires_at: string
  confirmed_at?: string
}
export type APIError = { error: { code: string; message: string } }
