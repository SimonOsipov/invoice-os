// Package customer is the workspace buyer directory: CUST's `customers` record.
package customer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var (
	ErrValidation = errors.New("customer: validation")
	ErrNotFound   = errors.New("customer: not found")
)

// validationError is an ErrValidation whose text is the 400 message.
type validationError string

func (e validationError) Error() string        { return string(e) }
func (e validationError) Is(target error) bool { return target == ErrValidation }

const (
	errNoFields  = validationError("no fields to update")
	errBuyerType = validationError("buyer_type must be B2B, B2G or B2C")
)

// Customer is a customers row. edited_fields is not on the wire.
type Customer struct {
	ID            string    `json:"id"`
	TIN           string    `json:"tin"`
	Name          *string   `json:"name"`
	ContactPerson *string   `json:"contact_person"`
	BuyerType     *string   `json:"buyer_type"`
	Email         *string   `json:"email"`
	Telephone     *string   `json:"telephone"`
	Street        *string   `json:"street"`
	City          *string   `json:"city"`
	PostalZone    *string   `json:"postal_zone"`
	Country       *string   `json:"country"`
	State         *string   `json:"state"`
	LGA           *string   `json:"lga"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListFilter is the Store.List query. TIN matches exactly after a trim.
type ListFilter struct {
	TIN    string
	Status string // "" lists both; otherwise "active" or "archived"
	Limit  int
	Offset int
}

// UpdateInput is the Store.Update argument: nil leaves a column unchanged.
type UpdateInput struct {
	Name          *string
	ContactPerson *string
	BuyerType     *string
	Email         *string
	Telephone     *string
	Street        *string
	City          *string
	PostalZone    *string
	Country       *string
	State         *string
	LGA           *string
}

type listPagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

type listResponse struct {
	Customers  []Customer     `json:"customers"`
	Pagination listPagination `json:"pagination"`
}

// ListHandler serves GET /v1/customers. A limit over 200 clamps; limit < 1 and offset < 0 are 400.
func ListHandler(list func(ctx context.Context, f ListFilter) ([]Customer, int, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		query := r.URL.Query()

		status := query.Get("status")
		if status != "" && status != "active" && status != "archived" {
			writeError(w, http.StatusBadRequest, `status must be "active" or "archived"`)
			return
		}

		limit := 50
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit > 200 {
			limit = 200
		} else if limit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be >= 1")
			return
		}

		offset := 0
		if raw := query.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "offset must be an integer")
				return
			}
			offset = n
		}
		if offset < 0 {
			writeError(w, http.StatusBadRequest, "offset must be >= 0")
			return
		}

		filter := ListFilter{TIN: query.Get("tin"), Status: status, Limit: limit, Offset: offset}
		items, total, err := list(r.Context(), filter)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "customer: list customers", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, listResponse{
			Customers:  items,
			Pagination: listPagination{Limit: filter.Limit, Offset: filter.Offset, Total: total},
		})
	}
}

type updateRequest struct {
	Name          *string `json:"name"`
	ContactPerson *string `json:"contact_person"`
	BuyerType     *string `json:"buyer_type"`
	Email         *string `json:"email"`
	Telephone     *string `json:"telephone"`
	Street        *string `json:"street"`
	City          *string `json:"city"`
	PostalZone    *string `json:"postal_zone"`
	Country       *string `json:"country"`
	State         *string `json:"state"`
	LGA           *string `json:"lga"`
}

// UpdateHandler serves PATCH /v1/customers/{id}. Unknown keys, tin included, are ignored.
func UpdateHandler(update func(ctx context.Context, id string, in UpdateInput) (Customer, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req updateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		in := UpdateInput(req)
		if in == (UpdateInput{}) {
			writeError(w, http.StatusBadRequest, errNoFields.Error())
			return
		}
		c, err := update(r.Context(), r.PathValue("id"), in)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "customer: update customer", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
