GO ?= go
VERSION ?= dev

.PHONY: build test installer-test fmt vet release
build:
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o awgpanel ./cmd/awgpanel

test:
	$(GO) test ./...

installer-test:
	bash -n install.sh
	bash scripts/test-install.sh
	bash scripts/test-install-integration.sh

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

release:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/awgpanel-linux-amd64 ./cmd/awgpanel
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/awgpanel-linux-arm64 ./cmd/awgpanel
	cd dist && sha256sum awgpanel-linux-amd64 awgpanel-linux-arm64 > SHA256SUMS
