.PHONY: build test lint fmt hooks hook-pre-commit hook-commit-msg schema-gen

build:
	CGO_ENABLED=0 go build -o soda ./cmd/soda

test:
	go test ./...

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
