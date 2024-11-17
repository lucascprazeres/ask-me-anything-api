tools:
	go install github.com/swaggo/swag/cmd/swag@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/jackc/tern/v2@latest

new-migration:
	tern new --migrations ./internal/store/pgstore/migrations

migrate:
	tern migrate --migrations ./internal/store/pgstore/migrations --config ./internal/store/pgstore/migrations/tern.conf

dev:
	air