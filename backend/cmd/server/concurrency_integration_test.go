package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Run against a dedicated PostgreSQL test database after applying migrations:
// TEST_DATABASE_URL='postgres://...' go test -race -run TestConcurrentReservationNeverOversells ./cmd/server
func TestConcurrentReservationNeverOversells(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL concurrency integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	itemID := newID("test_item")
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory(item_id,total_stock,reserved_stock) VALUES($1,1,0)`, itemID); err != nil {
		t.Fatalf("insert test inventory: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM reservations WHERE item_id = $1`, itemID)
		_, _ = db.Exec(`DELETE FROM inventory WHERE item_id = $1`, itemID)
	}()

	gin.SetMode(gin.TestMode)
	api := &API{db: db, cfg: Config{ReservationTTL: 5 * time.Minute, ExpiryInterval: time.Second}}
	router := gin.New()
	router.POST("/api/v1/inventory/reserve", api.reserve)

	const concurrentRequests = 40
	statuses := make(chan int, concurrentRequests)
	var wg sync.WaitGroup
	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload, _ := json.Marshal(reserveRequest{IdempotencyKey: newID("idem"), UserID: fmt.Sprintf("user_%d", i), ItemID: itemID, Quantity: 1})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reserve", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			statuses <- rec.Code
			if rec.Code != http.StatusCreated && rec.Code != http.StatusConflict {
				t.Errorf("unexpected response status %d: %s", rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(statuses)
	created, conflict := 0, 0
	for status := range statuses {
		if status == http.StatusCreated {
			created++
		}
		if status == http.StatusConflict {
			conflict++
		}
	}
	if created != 1 || conflict != concurrentRequests-1 {
		t.Fatalf("expected one reservation and %d conflicts; got created=%d conflict=%d", concurrentRequests-1, created, conflict)
	}
	var total, reserved int64
	if err := db.QueryRowContext(ctx, `SELECT total_stock,reserved_stock FROM inventory WHERE item_id=$1`, itemID).Scan(&total, &reserved); err != nil {
		t.Fatal(err)
	}
	if total != 1 || reserved != 1 || total-reserved < 0 {
		t.Fatalf("inventory invariant violated: total=%d reserved=%d available=%d", total, reserved, total-reserved)
	}
}

func TestExpiredReservationReleasesStockOnlyOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	itemID := newID("test_expiry_item")
	reservationID := newID("test_expiry_reservation")

	_, err = db.ExecContext(ctx, `
		INSERT INTO inventory(item_id, total_stock, reserved_stock)
		VALUES ($1, 5, 2)`, itemID)
	if err != nil {
		t.Fatalf("insert inventory: %v", err)
	}

	defer func() {
		_, _ = db.Exec(`DELETE FROM reservations WHERE item_id = $1`, itemID)
		_, _ = db.Exec(`DELETE FROM inventory WHERE item_id = $1`, itemID)
	}()

	_, err = db.ExecContext(ctx, `
		INSERT INTO reservations(
			reservation_id, user_id, item_id, quantity, status, expires_at
		)
		VALUES ($1, $2, $3, 2, 'active', NOW() - INTERVAL '1 minute')`,
		reservationID, "test_expiry_user", itemID)
	if err != nil {
		t.Fatalf("insert expired reservation: %v", err)
	}

	api := &API{db: db}
	if err := api.expireItem(ctx, itemID); err != nil {
		t.Fatalf("expire reservation: %v", err)
	}

	// Run expiry again. It must not release the same stock twice.
	if err := api.expireItem(ctx, itemID); err != nil {
		t.Fatalf("repeat expiry: %v", err)
	}

	var total, reserved int64
	var status string

	err = db.QueryRowContext(ctx, `
		SELECT i.total_stock, i.reserved_stock, r.status
		FROM inventory i
		JOIN reservations r ON r.item_id = i.item_id
		WHERE i.item_id = $1 AND r.reservation_id = $2`,
		itemID, reservationID).Scan(&total, &reserved, &status)
	if err != nil {
		t.Fatalf("read inventory and reservation: %v", err)
	}

	if status != "expired" {
		t.Fatalf("expected expired status, got %q", status)
	}
	if total != 5 || reserved != 0 {
		t.Fatalf(
			"unexpected stock after expiry: total=%d reserved=%d",
			total, reserved,
		)
	}
}

func TestConfirmReservationDeductsStockOnlyOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	itemID := newID("test_confirm_item")
	reservationID := newID("test_confirm_reservation")

	_, err = db.ExecContext(ctx, `
		INSERT INTO inventory(item_id, total_stock, reserved_stock)
		VALUES ($1, 5, 2)`, itemID)
	if err != nil {
		t.Fatalf("insert inventory: %v", err)
	}

	defer func() {
		_, _ = db.Exec(`DELETE FROM reservations WHERE item_id = $1`, itemID)
		_, _ = db.Exec(`DELETE FROM inventory WHERE item_id = $1`, itemID)
	}()

	_, err = db.ExecContext(ctx, `
		INSERT INTO reservations(
			reservation_id, user_id, item_id, quantity, status, expires_at
		)
		VALUES ($1, $2, $3, 2, 'active', NOW() + INTERVAL '5 minutes')`,
		reservationID, "test_confirm_user", itemID)
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}

	api := &API{db: db}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/inventory/confirm", api.confirm)

	requestBody, err := json.Marshal(confirmRequest{
		ReservationID: reservationID,
	})
	if err != nil {
		t.Fatal(err)
	}

	sendConfirm := func() int {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/inventory/confirm",
			bytes.NewReader(requestBody),
		)
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := sendConfirm(); got != http.StatusOK {
		t.Fatalf("first confirmation: expected 200, got %d", got)
	}

	// A repeated confirmation must not deduct stock again.
	if got := sendConfirm(); got != http.StatusConflict {
		t.Fatalf("second confirmation: expected 409, got %d", got)
	}

	var total, reserved int64
	var status string

	err = db.QueryRowContext(ctx, `
		SELECT i.total_stock, i.reserved_stock, r.status
		FROM inventory i
		JOIN reservations r ON r.item_id = i.item_id
		WHERE i.item_id = $1 AND r.reservation_id = $2`,
		itemID, reservationID).Scan(&total, &reserved, &status)
	if err != nil {
		t.Fatalf("read inventory and reservation: %v", err)
	}

	if status != "confirmed" {
		t.Fatalf("expected confirmed status, got %q", status)
	}
	if total != 3 || reserved != 0 {
		t.Fatalf(
			"unexpected stock after confirmation: total=%d reserved=%d",
			total, reserved,
		)
	}
}

func TestExpiredReservationConfirmRacePreservesStock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	itemID := newID("test_race_item")
	reservationID := newID("test_race_reservation")

	_, err = db.ExecContext(ctx, `
		INSERT INTO inventory(item_id, total_stock, reserved_stock)
		VALUES ($1, 5, 2)`, itemID)
	if err != nil {
		t.Fatalf("insert inventory: %v", err)
	}

	defer func() {
		_, _ = db.Exec(`DELETE FROM reservations WHERE item_id = $1`, itemID)
		_, _ = db.Exec(`DELETE FROM inventory WHERE item_id = $1`, itemID)
	}()

	_, err = db.ExecContext(ctx, `
		INSERT INTO reservations(
			reservation_id, user_id, item_id, quantity, status, expires_at
		)
		VALUES ($1, $2, $3, 2, 'active', NOW() - INTERVAL '1 minute')`,
		reservationID, "test_race_user", itemID)
	if err != nil {
		t.Fatalf("insert expired reservation: %v", err)
	}

	api := &API{db: db}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/inventory/confirm", api.confirm)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 1)

	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		if err := api.expireItem(ctx, itemID); err != nil {
			errs <- fmt.Errorf("expireItem: %w", err)
		}
	}()

	go func() {
		defer wg.Done()
		<-start

		body, _ := json.Marshal(confirmRequest{ReservationID: reservationID})
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/inventory/confirm",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			errs <- fmt.Errorf("confirm status: expected 409, got %d: %s",
				rec.Code, rec.Body.String())
		}
	}()

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	var total, reserved int64
	var status string
	err = db.QueryRowContext(ctx, `
		SELECT i.total_stock, i.reserved_stock, r.status
		FROM inventory i
		JOIN reservations r ON r.item_id = i.item_id
		WHERE i.item_id = $1 AND r.reservation_id = $2`,
		itemID, reservationID).Scan(&total, &reserved, &status)
	if err != nil {
		t.Fatal(err)
	}

	if status != "expired" || total != 5 || reserved != 0 {
		t.Fatalf("invariant violated: status=%s total=%d reserved=%d",
			status, total, reserved)
	}
}

// TestReservationIdempotency verifies retries do not reserve stock twice.
func TestReservationIdempotency(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	itemID := newID("test_idempotency_item")
	key := newID("test_idempotency_key")

	if _, err := db.ExecContext(ctx,
		`INSERT INTO inventory(item_id,total_stock,reserved_stock) VALUES($1,5,0)`,
		itemID,
	); err != nil {
		t.Fatalf("insert test inventory: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM reservations WHERE item_id = $1`, itemID)
		_, _ = db.Exec(`DELETE FROM inventory WHERE item_id = $1`, itemID)
	}()

	gin.SetMode(gin.TestMode)
	api := &API{db: db, cfg: Config{
		ReservationTTL: 5 * time.Minute,
		ExpiryInterval: time.Second,
	}}
	router := gin.New()
	router.POST("/api/v1/inventory/reserve", api.reserve)

	send := func(userID string, quantity int64) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"idempotency_key": key,
			"user_id":         userID,
			"item_id":         itemID,
			"quantity":        quantity,
		})
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/inventory/reserve",
			bytes.NewReader(payload),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := send("user_idem_1", 1)
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: got %d, body=%s", first.Code, first.Body.String())
	}

	var firstBody struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if firstBody.ReservationID == "" {
		t.Fatal("first response has empty reservation_id")
	}

	retry := send("user_idem_1", 1)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry: got %d, body=%s", retry.Code, retry.Body.String())
	}

	var retryBody struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retryBody); err != nil {
		t.Fatal(err)
	}
	if retryBody.ReservationID != firstBody.ReservationID {
		t.Fatalf("retry returned a different reservation: first=%s retry=%s",
			firstBody.ReservationID, retryBody.ReservationID)
	}

	// Reusing the same key with a different payload must be rejected.
	mismatch := send("another_user", 1)
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("different payload: got %d, want %d; body=%s",
			mismatch.Code, http.StatusConflict, mismatch.Body.String())
	}

	var reserved int64
	if err := db.QueryRowContext(ctx,
		`SELECT reserved_stock FROM inventory WHERE item_id = $1`,
		itemID,
	).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 1 {
		t.Fatalf("reserved_stock=%d, want 1 after retry", reserved)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM reservations WHERE idempotency_key = $1`,
		key,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reservations with the same key=%d, want 1", count)
	}
}
