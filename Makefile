SHELL := /bin/bash
GO    := /usr/local/go/bin/go
PORT  ?= 8001

.PHONY: all tidy build run dev web-install web-build web-dev clean

all: web-build build

tidy:
	$(GO) mod tidy

build:
	$(GO) build -o bin/agent ./cmd/agent

run: build
	PORT=$(PORT) ./bin/agent

dev:
	PORT=$(PORT) $(GO) run ./cmd/agent

web-install:
	cd web && npm install

web-build:
	cd web && npm run build

web-dev:
	cd web && npm run dev

clean:
	rm -rf bin web/dist
