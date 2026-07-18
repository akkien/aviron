package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/akkien/aviron/internal/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type registerResponse struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// Register godoc
// @Summary Register a new user
// @Description Creates a new user account with an email, password, and display name
// @Tags auth
// @Accept json
// @Produce json
// @Param request body registerRequest true "Registration payload"
// @Success 201 {object} registerResponse
// @Failure 400 {object} map[string]interface{} "field-keyed validation errors"
// @Failure 409 {object} map[string]string "error: email_taken"
// @Router /auth/register [post]
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	user, fieldErrs, err := h.svc.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if len(fieldErrs) > 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
		return
	}
	if errors.Is(err, ErrEmailTaken) {
		httpx.WriteError(w, http.StatusConflict, "email_taken")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, registerResponse{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
	})
}
