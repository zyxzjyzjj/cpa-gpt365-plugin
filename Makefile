PLUGIN_ID := gpt365
VERSION   := 0.1.0
GOOS      ?= $(shell go env GOOS)
GOARCH    ?= $(shell go env GOARCH)

ifeq ($(GOOS),windows)
EXT := .dll
else ifeq ($(GOOS),darwin)
EXT := .dylib
else
EXT := .so
endif

OUTDIR := build/$(GOOS)/$(GOARCH)
TARGET := $(OUTDIR)/$(PLUGIN_ID)$(EXT)

LDFLAGS := -s -w -buildid=

.PHONY: all build test test-live vet fmt clean release-assets

all: build

build:
	mkdir -p $(OUTDIR)
	CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" \
		-buildmode=c-shared -o $(TARGET) .
	rm -f $(OUTDIR)/$(PLUGIN_ID).h
	@echo "已生成 $(TARGET)"

test:
	CGO_ENABLED=1 go test ./...

# 真实链路集成测试：需要本机两级代理链可用，并显式提供凭据。
#   BP_LIVE=1 BP_TOKEN=... BP_ACCOUNT_ID=... make test-live
test-live:
	CGO_ENABLED=1 BP_LIVE=1 go test ./internal/basispoints/ -run TestLive -v -timeout 900s

vet:
	CGO_ENABLED=1 go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf build dist

# 本地生成与 CI 一致的发布压缩包，便于在没有 CI 的情况下验证打包契约。
release-assets: build
	mkdir -p dist
	cp $(TARGET) dist/
	cd dist && zip -j $(PLUGIN_ID)_$(VERSION)_$(GOOS)_$(GOARCH).zip $(PLUGIN_ID)$(EXT)
	@echo "已生成 dist/$(PLUGIN_ID)_$(VERSION)_$(GOOS)_$(GOARCH).zip"
