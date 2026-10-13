package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Addr           string
	DatabaseURL    string
	CORSOrigin     string
	ExpiryInterval time.Duration
	ReservationTTL time.Duration
	MaxOpenConns   int
	MaxIdleConns   int
}

func loadConfig() Config {
	return Config{
		Addr:           env("APP_ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://inventory:inventory_dev_password@localhost:5432/inventory?sslmode=disable"),
		CORSOrigin:     env("CORS_ORIGIN", "http://localhost:5173"),
		ExpiryInterval: envDuration("EXPIRY_INTERVAL", 2*time.Second),
		ReservationTTL: envDuration("RESERVATION_TTL", 5*time.Minute),
		MaxOpenConns:   envInt("DB_MAX_OPEN_CONNS", 20),
		MaxIdleConns:   envInt("DB_MAX_IDLE_CONNS", 10),
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return d
}
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		log.Fatalf("invalid %s", key)
	}
	return n
}

type API struct {
	db  *sql.DB
	cfg Config
}

type reserveRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	UserID         string `json:"user_id" binding:"required"`
	ItemID         string `json:"item_id" binding:"required"`
	Quantity       int64  `json:"quantity" binding:"required,gt=0"`
}
type confirmRequest struct {
	ReservationID string `json:"reservation_id" binding:"required"`
}

type apiError struct {
	Error errorBody `json:"error"`
}
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, apiError{Error: errorBody{Code: code, Message: message}})
}

func main() {
	cfg := loadConfig()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.PingContext(ctx); err != nil {
		cancel()
		log.Fatalf("ping database: %v", err)
	}
	cancel()

	api := &API{db: db, cfg: cfg}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	go api.expiryWorker(workerCtx)

	gin.SetMode(env("GIN_MODE", gin.ReleaseMode))
	router := gin.New()
	router.Use(gin.Recovery(), requestLogger(), cors(cfg.CORSOrigin))
	router.GET("/healthz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			writeError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "Database is unavailable")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	v1 := router.Group("/api/v1")
	{
		v1.POST("/inventory/reserve", api.reserve)
		v1.POST("/inventory/confirm", api.confirm)
		v1.GET("/inventory/stock", api.stock)
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("API listening on %s", cfg.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-sigCtx.Done():
		log.Println("shutdown signal received")
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	stopWorker()
	db.Close()
	log.Println("shutdown complete")
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("%s %s status=%d duration=%s", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(start))
	}
}
func cors(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Vary", "Origin")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

type reservationByKey struct {
	ID        string
	UserID    string
	ItemID    string
	Quantity  int64
	Status    string
	ExpiresAt time.Time
}

type reservationKeyQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupReservationKey(
	ctx context.Context,
	q reservationKeyQuerier,
	key string,
) (reservationByKey, error) {
	var r reservationByKey
	err := q.QueryRowContext(ctx, `
		SELECT reservation_id, user_id, item_id, quantity, status, expires_at
		FROM reservations
		WHERE idempotency_key = $1
	`, key).Scan(&r.ID, &r.UserID, &r.ItemID, &r.Quantity, &r.Status, &r.ExpiresAt)
	return r, err
}

func reservationPayloadMatches(r reservationByKey, req reserveRequest) bool {
	return r.UserID == req.UserID &&
		r.ItemID == req.ItemID &&
		r.Quantity == req.Quantity
}

func respondWithReservation(c *gin.Context, r reservationByKey) {
	c.JSON(http.StatusOK, gin.H{
		"status":             "success",
		"reservation_id":     r.ID,
		"item_id":            r.ItemID,
		"quantity":           r.Quantity,
		"expires_at":         r.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"reservation_status": r.Status,
	})
}

func (a *API) reserve(c *gin.Context) {
	var req reserveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST",
			"idempotency_key, user_id, item_id, and a positive quantity are required")
		return
	}

	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.UserID = strings.TrimSpace(req.UserID)
	req.ItemID = strings.TrimSpace(req.ItemID)

	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 ||
		req.UserID == "" || req.ItemID == "" || req.Quantity <= 0 {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST",
			"idempotency_key must be 1-200 characters; user_id and item_id must be non-empty; quantity must be positive")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()

	// Fast path: return the existing reservation for a retried request.
	existing, err := lookupReservationKey(ctx, a.db, req.IdempotencyKey)
	if err == nil {
		if !reservationPayloadMatches(existing, req) {
			writeError(c, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED",
				"This idempotency_key was already used with a different payload")
			return
		}
		respondWithReservation(c, existing)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not check idempotency key")
		return
	}

	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not start transaction")
		return
	}
	defer tx.Rollback()

	// Keep the established lock order: inventory row before reservation changes.
	var itemID string
	err = tx.QueryRowContext(ctx,
		`SELECT item_id FROM inventory WHERE item_id = $1 FOR UPDATE`,
		req.ItemID,
	).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(c, http.StatusNotFound, "ITEM_NOT_FOUND", "Inventory item was not found")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not lock inventory")
		return
	}

	// Recheck after locking inventory to handle concurrent retries for this item.
	existing, err = lookupReservationKey(ctx, tx, req.IdempotencyKey)
	if err == nil {
		if !reservationPayloadMatches(existing, req) {
			writeError(c, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED",
				"This idempotency_key was already used with a different payload")
			return
		}
		respondWithReservation(c, existing)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not check idempotency key")
		return
	}

	if err := expireForItem(ctx, tx, req.ItemID); err != nil {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not expire stale reservations")
		return
	}

	var total, reserved int64
	err = tx.QueryRowContext(ctx, `
		UPDATE inventory
		SET reserved_stock = reserved_stock + $1, updated_at = NOW()
		WHERE item_id = $2 AND total_stock - reserved_stock >= $1
		RETURNING total_stock, reserved_stock`,
		req.Quantity, req.ItemID,
	).Scan(&total, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(c, http.StatusConflict, "INSUFFICIENT_STOCK", "Available stock is insufficient")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not reserve stock")
		return
	}

	reservationID := newID("res")
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO reservations
			(reservation_id, user_id, item_id, quantity, status, expires_at, idempotency_key)
		VALUES ($1, $2, $3, $4, 'active',
			NOW() + ($5 * INTERVAL '1 second'), $6)
		RETURNING expires_at`,
		reservationID, req.UserID, req.ItemID, req.Quantity,
		int64(a.cfg.ReservationTTL.Seconds()), req.IdempotencyKey,
	).Scan(&expiresAt)

	if err != nil {
		// Roll back the stock update before resolving a concurrent duplicate key.
		_ = tx.Rollback()
		existing, lookupErr := lookupReservationKey(ctx, a.db, req.IdempotencyKey)
		if lookupErr == nil {
			if !reservationPayloadMatches(existing, req) {
				writeError(c, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED",
					"This idempotency_key was already used with a different payload")
				return
			}
			respondWithReservation(c, existing)
			return
		}
		writeError(c, http.StatusInternalServerError, "DATABASE_ERROR",
			"Could not create reservation")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(c, http.StatusInternalServerError, "COMMIT_FAILED",
			"Could not commit reservation; retry with the same idempotency_key")
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":             "success",
		"reservation_id":     reservationID,
		"item_id":            req.ItemID,
		"quantity":           req.Quantity,
		"expires_at":         expiresAt.UTC().Format(time.RFC3339Nano),
		"reservation_status": "active",
	})
}

func (a *API) confirm(c *gin.Context) {
	var req confirmRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ReservationID) == "" {
		writeError(c, 400, "INVALID_REQUEST", "reservation_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()

	// Read item_id first without locking; all mutating flows lock inventory before reservation rows.
	var itemID string
	err := a.db.QueryRowContext(ctx, `SELECT item_id FROM reservations WHERE reservation_id = $1`, req.ReservationID).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(c, 404, "RESERVATION_NOT_FOUND", "Reservation was not found")
		return
	}
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not read reservation")
		return
	}

	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not start transaction")
		return
	}
	defer tx.Rollback()
	var lockedItem string
	err = tx.QueryRowContext(ctx, `SELECT item_id FROM inventory WHERE item_id = $1 FOR UPDATE`, itemID).Scan(&lockedItem)
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not lock inventory")
		return
	}
	if err := expireForItem(ctx, tx, itemID); err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not expire stale reservations")
		return
	}

	var quantity int64
	var confirmedAt time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE reservations
		SET status = 'confirmed', confirmed_at = NOW()
		WHERE reservation_id = $1 AND status = 'active' AND expires_at > NOW()
		RETURNING quantity, confirmed_at`, req.ReservationID).Scan(&quantity, &confirmedAt)
	if errors.Is(err, sql.ErrNoRows) {
		var status string
		var expires time.Time
		err2 := tx.QueryRowContext(ctx, `SELECT status, expires_at FROM reservations WHERE reservation_id = $1`, req.ReservationID).Scan(&status, &expires)
		if err2 == nil && status == "confirmed" {
			writeError(c, 409, "ALREADY_CONFIRMED", "Reservation has already been confirmed")
		} else if err2 == nil && (status == "expired" || !expires.After(time.Now())) {
			writeError(c, 409, "RESERVATION_EXPIRED", "Reservation has expired")
		} else {
			writeError(c, 409, "RESERVATION_NOT_ACTIVE", "Reservation is not active")
		}
		return
	}
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not confirm reservation")
		return
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE inventory
		SET total_stock = total_stock - $1,
		    reserved_stock = reserved_stock - $1,
		    updated_at = NOW()
		WHERE item_id = $2 AND total_stock >= $1 AND reserved_stock >= $1`, quantity, itemID)
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not commit inventory")
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected != 1 {
		writeError(c, 500, "INVENTORY_INVARIANT_VIOLATION", "Inventory could not be updated consistently")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(c, 500, "COMMIT_FAILED", "Could not commit confirmation")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "reservation_id": req.ReservationID, "confirmed_at": confirmedAt.UTC().Format(time.RFC3339Nano)})
}

func (a *API) stock(c *gin.Context) {
	itemID := strings.TrimSpace(c.Query("item_id"))
	if itemID == "" {
		writeError(c, 400, "INVALID_ITEM_ID", "item_id query parameter is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	var total, reserved int64
	err := a.db.QueryRowContext(ctx, `SELECT total_stock, reserved_stock FROM inventory WHERE item_id = $1`, itemID).Scan(&total, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(c, 404, "ITEM_NOT_FOUND", "Inventory item was not found")
		return
	}
	if err != nil {
		writeError(c, 500, "DATABASE_ERROR", "Could not query inventory")
		return
	}
	c.JSON(http.StatusOK, gin.H{"item_id": itemID, "total_stock": total, "reserved_stock": reserved, "available_stock": total - reserved})
}

// Caller must lock the inventory row before calling this function.
func expireForItem(ctx context.Context, tx *sql.Tx, itemID string) error {
	rows, err := tx.QueryContext(ctx, `
		UPDATE reservations SET status = 'expired'
		WHERE item_id = $1 AND status = 'active' AND expires_at <= NOW()
		RETURNING quantity`, itemID)
	if err != nil {
		return err
	}
	var released int64
	for rows.Next() {
		var q int64
		if err := rows.Scan(&q); err != nil {
			rows.Close()
			return err
		}
		released += q
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if released > 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE inventory
			SET reserved_stock = reserved_stock - $1,
				updated_at = NOW()
			WHERE item_id = $2
			AND reserved_stock >= $1`,
			released, itemID,
		)
		if err != nil {
			return err
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected != 1 {
			return fmt.Errorf(
				"inventory invariant violation while expiring reservations: item_id=%s released=%d",
				itemID, released,
			)
		}
	}
	return nil
}

func (a *API) expiryWorker(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.ExpiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.expireBatch(ctx)
		}
	}
}
func (a *API) expireBatch(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := a.db.QueryContext(queryCtx, `
		SELECT DISTINCT item_id FROM reservations
		WHERE status = 'active' AND expires_at <= NOW()
		LIMIT 100`)
	if err != nil {
		log.Printf("expiry scan: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return
	}
	rows.Close()
	for _, id := range ids {
		if err := a.expireItem(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("expire item %s: %v", id, err)
		}
	}
}
func (a *API) expireItem(parent context.Context, itemID string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT item_id FROM inventory WHERE item_id = $1 FOR UPDATE`, itemID).Scan(&id); err != nil {
		return err
	}
	if err := expireForItem(ctx, tx, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b)
}
