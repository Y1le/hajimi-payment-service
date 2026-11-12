package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/Y1le/hajimi-payment-service/internal/order"
)

type OrderStore interface {
	Create(order.CreateRequest) (order.Order, bool, error)
	Get(string) (order.Order, error)
}

// New builds the independent service's routes. A single API key represents one
// trusted merchant backend in this initial version; it must never reach a browser.
func New(store OrderStore, apiKey string) (http.Handler, error) {
	if len(apiKey) < 32 {
		return nil, errors.New("PAYMENT_API_KEY must contain at least 32 bytes")
	}
	s := &server{store: store, authHash: sha256.Sum256([]byte("Bearer " + apiKey))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /api/v1/orders", s.authenticate(http.HandlerFunc(s.createOrder)))
	mux.Handle("GET /api/v1/orders/{id}", s.authenticate(http.HandlerFunc(s.getOrder)))
	return mux, nil
}

type server struct {
	store    OrderStore
	authHash [32]byte
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], s.authHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid merchant API key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "INVALID_CONTENT_TYPE", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req order.CreateRequest
	if err := decoder.Decode(&req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeDecodeError(w, err)
		return
	}
	created, replayed, err := s.store.Create(req)
	if err != nil {
		switch {
		case errors.Is(err, order.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "INVALID_ORDER", err.Error())
		case errors.Is(err, order.ErrConflict):
			writeError(w, http.StatusConflict, "ORDER_CONFLICT", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not create order")
		}
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/orders/"+created.ID)
	writeJSON(w, status, struct {
		Order    order.Order `json:"order"`
		Replayed bool        `json:"replayed"`
	}{Order: created, Replayed: replayed})
}

func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	found, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
		} else {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not get order")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Order order.Order `json:"order"`
	}{Order: found})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body exceeds 16 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "INVALID_JSON", "body must contain one JSON object with the documented fields")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// Headers are already sent; an interrupted connection cannot be retried here.
	_ = json.NewEncoder(w).Encode(value)
}
