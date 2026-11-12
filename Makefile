VERSION ?= 0.3.1
LDFLAGS = -s -w -X github.com/clisboa/goodreadsmark/internal/app.Version=$(VERSION)

.PHONY: build cross test integration race lint fmt vet verify vuln privacy run

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/goodreadsmark ./cmd/goodreadsmark

cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/goodreadsmark-linux-amd64 ./cmd/goodreadsmark
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/goodreadsmark-linux-arm64 ./cmd/goodreadsmark
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/goodreadsmark-darwin-arm64 ./cmd/goodreadsmark

test:
	go test ./...

integration:
	go test -tags=integration -count=1 ./...

race:
	go test -tags=integration -race -shuffle=on -count=1 ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

vet:
	go vet ./...

verify: test integration race lint vet
	go mod verify
	test -z "$$(gofmt -l cmd internal)"

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

privacy:
	go run ./cmd/privacycheck
	go run ./cmd/privacycheck --history

run: build
	./bin/goodreadsmark --env-file .env run
