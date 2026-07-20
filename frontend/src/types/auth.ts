// Mirrors backend/internal/auth/dtos.go

export interface LoginRequest {
  email: string
  password: string
}

export interface LoginResponse {
  token: string
  expires_at: string
}
