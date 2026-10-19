.PHONY: test demo build dist
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

test:  ; go vet ./... && go test ./...
demo:  ; go run ./cmd/cowboy demo
build: ; go build -o bin/cowboy ./cmd/cowboy

# the release downloads; the release workflow runs this on every v* tag
dist:
	rm -rf dist && mkdir -p dist
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/cowboy-darwin-arm64 ./cmd/cowboy
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/cowboy-darwin-amd64 ./cmd/cowboy
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/cowboy-linux-amd64 ./cmd/cowboy
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/cowboy-linux-arm64 ./cmd/cowboy
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/cowboy-windows-amd64.exe ./cmd/cowboy
	cp scripts/install.sh dist/install.sh
	cd dist && sha256sum cowboy-* install.sh > SHA256SUMS
