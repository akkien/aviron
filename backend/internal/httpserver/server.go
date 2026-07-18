package httpserver

import "net/http"

func NewServer() *http.ServeMux {
	server := http.NewServeMux()
	return server
}
