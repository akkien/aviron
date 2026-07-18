run:
	go run ./cmd/server

test:
	go test ./... -race

docs:
	swag init -g cmd/server/main.go --parseDependency --parseInternal
