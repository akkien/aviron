.PHONY: run build start stop restart test docs

BINARY := server
PID_FILE := .server.pid
LOG_FILE := server.log

run:
	go run ./cmd/server

build:
	go build -o $(BINARY) ./cmd/server

start: build
	@if [ -f $(PID_FILE) ] && kill -0 $$(cat $(PID_FILE)) 2>/dev/null; then \
		echo "server already running (pid $$(cat $(PID_FILE)))"; \
	else \
		./$(BINARY) > $(LOG_FILE) 2>&1 & echo $$! > $(PID_FILE); \
		echo "server started (pid $$(cat $(PID_FILE))), logs: $(LOG_FILE)"; \
	fi

stop:
	@if [ -f $(PID_FILE) ]; then \
		kill $$(cat $(PID_FILE)) 2>/dev/null || true; \
		rm -f $(PID_FILE); \
		echo "server stopped"; \
	else \
		echo "server not running (no $(PID_FILE))"; \
	fi

restart: stop start

test:
	go test ./... -race

docs:
	swag init -g cmd/server/main.go --parseDependency --parseInternal
