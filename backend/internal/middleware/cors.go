package middleware

import "net/http"

// Cors allows cross-origin requests from allowedOrigin (the Vite dev
// server's origin, a different port than the API in local development) and
// answers preflight OPTIONS requests directly, since http.ServeMux's
// method-based routing never matches OPTIONS against a route registered as
// e.g. "POST /races".
func Cors(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
