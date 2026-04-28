.PHONY: build test test-integration-sandbox lint fmt hooks hook-pre-commit hook-commit-msg schema-gen

build:
	CGO_ENABLED=0 go build -o soda ./cmd/soda

test:
	go test ./...

test-integration-sandbox:
	CGO_ENABLED=1 go test -tags 'cgo integration' -count=1 -timeout 5m -v ./internal/sandbox/

lint:
	go vet ./...

fmt:
	gofmt -w .

hooks: hook-pre-commit hook-commit-msg
	git config core.hooksPath .githooks
	@printf "Git hooks path set to .githooks\n"

hook-pre-commit:
	chmod +x .githooks/pre-commit
	@printf "Installed pre-commit hook\n"

hook-commit-msg:
	chmod +x .githooks/commit-msg
	@printf "Installed commit-msg hook\n"

schema-gen:
	go generate ./schemas/...
