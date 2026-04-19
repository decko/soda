.PHONY: build test lint fmt hooks schema-gen

build:
	CGO_ENABLED=0 go build -o soda ./cmd/soda

test:
	go test ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

hooks:
	./scripts/setup-hooks.sh

schema-gen:
	go generate ./schemas/...
