import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity, AlertCircle, ArrowDownRight, ArrowUpRight, Boxes, CheckCircle2,
  CircleHelp, Clock3, Command, Database, PackageCheck, RefreshCw, ShieldCheck,
  ShoppingCart, Zap,
} from 'lucide-react'
import { confirmReservation, getStock, reserveStock } from './api'
import type { Reservation, Stock } from './types'

const ITEM_ID = 'item_4021'

function formatNumber(value: number) {
  return new Intl.NumberFormat('id-ID').format(value)
}
function formatTime(value: string) {
  return new Date(value).toLocaleTimeString('id-ID', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
function App() {
  const [stock, setStock] = useState<Stock | null>(null)
  const [userId, setUserId] = useState('usr_9981')
  const idempotencyRef = useRef<{ fingerprint: string; key: string } | null>(null)
  const [quantity, setQuantity] = useState(1)
  const [reservation, setReservation] = useState<Reservation | null>(null)
  const [secondsLeft, setSecondsLeft] = useState(0)
  const [loadingStock, setLoadingStock] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  const loadStock = useCallback(async (quiet = false) => {
    if (!quiet) setLoadingStock(true)
    try {
      const data = await getStock(ITEM_ID)
      setStock(data)
      setLastUpdated(new Date())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Gagal memuat stok')
    } finally {
      if (!quiet) setLoadingStock(false)
    }
  }, [])

  useEffect(() => {
    void loadStock()
    const id = window.setInterval(() => void loadStock(true), 5000)
    return () => window.clearInterval(id)
  }, [loadStock])

  useEffect(() => {
    if (!reservation || reservation.status !== 'active') {
      setSecondsLeft(0)
      return
    }
    const update = () => setSecondsLeft(Math.max(0, Math.ceil((new Date(reservation.expires_at).getTime() - Date.now()) / 1000)))
    update()
    const id = window.setInterval(update, 1000)
    return () => window.clearInterval(id)
  }, [reservation])

  const countdown = useMemo(() => {
    const mm = Math.floor(secondsLeft / 60).toString().padStart(2, '0')
    const ss = (secondsLeft % 60).toString().padStart(2, '0')
    return `${mm}:${ss}`
  }, [secondsLeft])

  async function handleReserve(event: React.FormEvent) {
    event.preventDefault()
    setError('')
    setNotice('')
    setSubmitting(true)

    const payload = {
      user_id: userId.trim(),
      item_id: ITEM_ID,
      quantity,
    }
    const fingerprint = JSON.stringify(payload)

    if (!idempotencyRef.current || idempotencyRef.current.fingerprint !== fingerprint) {
      idempotencyRef.current = {
        fingerprint,
        key: crypto.randomUUID(),
      }
    }

    try {
      const data = await reserveStock({
        ...payload,
        idempotency_key: idempotencyRef.current.key,
      })

      idempotencyRef.current = null
      setReservation({
        reservation_id: data.reservation_id,
        item_id: data.item_id,
        quantity: data.quantity,
        expires_at: data.expires_at,
        status: data.reservation_status,
      })
      setNotice(
        data.reservation_status === 'active'
          ? 'Stok berhasil ditahan. Selesaikan konfirmasi sebelum waktu habis.'
          : `Request sudah diproses. Status reservasi: ${data.reservation_status}.`,
      )
      await loadStock(true)
    } catch (e) {
      // Keep the same key so the user can safely retry this exact payload.
      setError(e instanceof Error ? e.message : 'Reservasi gagal')
    } finally {
      setSubmitting(false)
    }
  }

  async function handleConfirm() {
    if (!reservation) return
    setError('')
    setNotice('')
    setSubmitting(true)
    try {
      const data = await confirmReservation(reservation.reservation_id)
      setReservation({ ...reservation, ...data, status: 'confirmed' })
      setNotice('Pembelian berhasil dikonfirmasi.')
      await loadStock(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Konfirmasi gagal')
      await loadStock(true)
    } finally {
      setSubmitting(false)
    }
  }

  const reservationActive = reservation?.status === 'active' && secondsLeft > 0
  const reservedPercent = stock && stock.total_stock > 0 ? Math.min(100, (stock.reserved_stock / stock.total_stock) * 100) : 0

  return (
    <div className="min-h-screen bg-ink text-slate-100">
      <aside className="sidebar">
        <div className="brand-mark"><Command size={19} /></div>
        <div className="brand-copy"><strong>Flash Sale Inventory</strong><span>Ops Console</span></div>
        <div className="sidebar-rule" />
        <div className="side-item active"><Boxes size={18} /><span>Inventory</span></div>
        <div className="sidebar-bottom">
          <div className="status-dot" />
          <div><strong>API connection</strong><span>Polling every 5 seconds</span></div>
        </div>
      </aside>

      <main className="main-content">
        <header className="topbar">
          <div className="breadcrumb"><span>Operations</span><span className="slash">/</span><strong>Inventory control</strong></div>
          <div className="topbar-right"><span className="env-pill"><span /> DEVELOPMENT</span><div className="avatar">S</div></div>
        </header>

        <div className="page-wrap">
          <section className="page-heading">
            <div>
              <div className="eyebrow"><Zap size={13} /> FLASH SALE CONTROL CENTER</div>
              <h1>Inventory <span>Overview</span></h1>
              <p>Real-time stock allocation and reservation monitoring.</p>
            </div>
            <button className="btn-secondary" onClick={() => void loadStock()} disabled={loadingStock}>
              <RefreshCw size={15} className={loadingStock ? 'spin' : ''} /> Refresh data
            </button>
          </section>

          {error && <div className="feedback error"><AlertCircle size={17} /><span>{error}</span><button onClick={() => setError('')} aria-label="Dismiss error">×</button></div>}
          {notice && <div className="feedback success"><CheckCircle2 size={17} /><span>{notice}</span><button onClick={() => setNotice('')} aria-label="Dismiss notice">×</button></div>}

          <section className="metrics-grid">
            <Metric label="Total inventory" value={stock ? formatNumber(stock.total_stock) : '—'} icon={<Boxes size={18} />} tone="blue" foot="Physical stock in database" />
            <Metric label="Reserved stock" value={stock ? formatNumber(stock.reserved_stock) : '—'} icon={<Clock3 size={18} />} tone="amber" foot="Currently held by users" change={<ArrowUpRight size={14} />} />
            <Metric label="Available stock" value={stock ? formatNumber(stock.available_stock) : '—'} icon={<PackageCheck size={18} />} tone="mint" foot="Available to reserve" change={<ArrowDownRight size={14} />} />
          </section>

          <section className="content-grid">
            <div className="panel inventory-panel">
              <div className="panel-head">
                <div><div className="section-kicker">INVENTORY MONITOR</div><h2>Stock allocation</h2></div>
                <span className="live-pill"><span /> LIVE</span>
              </div>
              <div className="product-line">
                <div className="product-icon"><PackageCheck size={24} /></div>
                <div className="product-meta"><strong>Flash Sale Item</strong><span>{ITEM_ID}</span></div>
                <span className="sku-tag">SKU · 4021</span>
              </div>
              <div className="allocation">
                <div className="allocation-title"><span>Stock utilization</span><strong>{reservedPercent.toFixed(1)}%</strong></div>
                <div className="progress-track"><div className="progress-value" style={{ width: `${reservedPercent}%` }} /></div>
                <div className="legend">
                  <span><i className="legend-dot mint" /> Available <b>{stock ? formatNumber(stock.available_stock) : '—'}</b></span>
                  <span><i className="legend-dot amber" /> Reserved <b>{stock ? formatNumber(stock.reserved_stock) : '—'}</b></span>
                </div>
              </div>
              <div className="invariant-box"><ShieldCheck size={17} /><div><strong>Inventory invariant protected</strong><span>Available stock = total stock − reserved stock</span></div></div>
              <div className="panel-foot"><span><span className="tiny-dot" /> PostgreSQL source of truth</span><span>{lastUpdated ? `Updated ${formatTime(lastUpdated.toISOString())}` : 'Waiting for data'}</span></div>
            </div>

            <div className="panel reserve-panel">
              <div className="panel-head">
                <div><div className="section-kicker">CHECKOUT SIMULATOR</div><h2>Reserve inventory</h2></div>
                <span className="step-number">01 / 02</span>
              </div>
              <form onSubmit={handleReserve} className="reserve-form">
                <label>User ID<input value={userId} onChange={e => setUserId(e.target.value)} required minLength={2} placeholder="usr_9981" /></label>
                <label>Item ID<input value={ITEM_ID} disabled /></label>
                <label>Quantity<div className="quantity-control"><button type="button" onClick={() => setQuantity(v => Math.max(1, v - 1))} aria-label="Decrease quantity">−</button><input type="number" value={quantity} min={1} max={1000000} onChange={e => setQuantity(Math.max(1, Number(e.target.value)))} /><button type="button" onClick={() => setQuantity(v => Math.min(1000000, v + 1))} aria-label="Increase quantity">+</button></div></label>
                <button className="btn-primary full-width" type="submit" disabled={submitting || !userId.trim()}><ShoppingCart size={16} /> {submitting ? 'Processing...' : 'Reserve stock'} <span>→</span></button>
                <p className="form-note"><ShieldCheck size={13} /> Atomic allocation · 5-minute hold · PostgreSQL transaction</p>
              </form>
            </div>
          </section>

          <section className="panel reservation-panel">
            <div className="panel-head">
              <div><div className="section-kicker">RESERVATION LIFECYCLE</div><h2>Active reservation</h2></div>
              <span className={`state-pill ${reservation?.status ?? 'idle'}`}><span /> {reservation?.status?.toUpperCase() ?? 'NO SESSION'}</span>
            </div>
            {!reservation ? (
              <div className="empty-state"><div className="empty-icon"><CircleHelp size={23} /></div><strong>No active reservation</strong><p>Submit a reservation to see the hold timer and confirmation workflow.</p></div>
            ) : (
              <div className="reservation-details">
                <div className="reservation-id"><span>RESERVATION ID</span><strong>{reservation.reservation_id}</strong><span className="subtext">Item {reservation.item_id} · Quantity {reservation.quantity}</span></div>
                <div className={`timer-card ${secondsLeft <= 30 && reservation.status === 'active' ? 'urgent' : ''}`}>
                  <Clock3 size={19} /><div><span>{reservation.status === 'active' ? 'TIME REMAINING' : reservation.status === 'confirmed' ? 'CONFIRMED AT' : 'RESERVATION EXPIRED'}</span><strong>{reservation.status === 'active' ? countdown : reservation.status === 'confirmed' && reservation.confirmed_at ? formatTime(reservation.confirmed_at) : '00:00'}</strong></div>
                </div>
                <div className="reservation-actions">
                  <button className="btn-secondary" onClick={() => void loadStock()}><RefreshCw size={15} /> Refresh stock</button>
                  <button className="btn-primary" onClick={() => void handleConfirm()} disabled={!reservationActive || submitting}><CheckCircle2 size={16} /> {submitting ? 'Processing...' : 'Confirm purchase'} <span>→</span></button>
                </div>
                {reservation.status === 'active' && secondsLeft === 0 && <p className="expired-note"><AlertCircle size={15} /> Timer elapsed. The server is authoritative; refresh stock and try again if needed.</p>}
              </div>
            )}
          </section>

          <footer className="footer"><span>T <b>·</b> Inventory Operations Console</span><span><span className="footer-dot" /> API polling enabled <b>·</b> Demo environment</span></footer>
        </div>
      </main>
    </div>
  )
}

function Metric({ label, value, icon, tone, foot, change }: { label: string; value: string; icon: React.ReactNode; tone: string; foot: string; change?: React.ReactNode }) {
  return <div className="metric-card"><div className="metric-top"><span>{label}</span><div className={`metric-icon ${tone}`}>{icon}</div></div><div className="metric-value">{value}{change && <span className={`metric-change ${tone}`}>{change}</span>}</div><div className="metric-foot"><span className={`metric-foot-dot ${tone}`} />{foot}</div></div>
}

export default App
