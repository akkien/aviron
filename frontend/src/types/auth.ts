// Mirrors backend/internal/auth/dtos.go

export interface LoginRequest {
  email: string
  password: string
}

export interface LoginResponse {
  token: string
  expires_at: string
}

export interface RegisterRequest {
  email: string
  password: string
  display_name: string
}

export interface RegisterResponse {
  id: string
  email: string
  display_name: string
}
