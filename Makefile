-include .deploy.mk

APP_NAME    ?= inkfetch
CMD_PKG     ?= ./cmd/web
BIN_DIR     ?= bin
BINARY      := $(BIN_DIR)/$(APP_NAME)

# Target architecture for VPS cross-compilation (override e.g. GOARCH=arm64)
GOOS        ?= linux
GOARCH      ?= amd64
LDFLAGS     ?= -s -w

# VPS deployment settings (configure once in .deploy.mk or pass on CLI)
VPS_HOST    ?=
VPS_PORT    ?= 22
VPS_DIR     ?= /opt/$(APP_NAME)
VPS_SERVICE ?= $(APP_NAME)
VPS_USER    ?=
SSH_KEY     ?=
SUDO        ?= sudo

SSH_OPTS := -p $(VPS_PORT)
ifneq ($(strip $(SSH_KEY)),)
SSH_OPTS += -i $(SSH_KEY)
endif

SSH   := ssh $(SSH_OPTS)
RSYNC := rsync -az --human-readable -e "ssh $(SSH_OPTS)"

.DEFAULT_GOAL := help

.PHONY: help run build build-linux vet test clean \
	check-vps setup-vps deploy deploy-fast _upload-and-restart \
	sync-env sync-tls status logs restart stop ssh backup-db

help: ## Show available targets and deployment configuration
	@echo "InkFetch Build & VPS Deploy"
	@echo ""
	@echo "Usage: make <target> [VPS_HOST=user@host] [GOARCH=amd64|arm64]"
	@echo ""
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Config (.deploy.mk or env/CLI overrides):"
	@echo "  VPS_HOST=$(if $(VPS_HOST),$(VPS_HOST),(unset — copy .deploy.mk.example to .deploy.mk))"
	@echo "  VPS_PORT=$(VPS_PORT)  VPS_DIR=$(VPS_DIR)  VPS_SERVICE=$(VPS_SERVICE)"
	@echo "  GOOS=$(GOOS)  GOARCH=$(GOARCH)  SUDO=$(SUDO)"

run: ## Run InkFetch locally with go run
	go run $(CMD_PKG)

build: ## Build binary for the current host OS/arch
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(CMD_PKG)
	@echo "Built $(BINARY)"

build-linux: ## Cross-compile static Linux binary for VPS (CGO_ENABLED=0)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(CMD_PKG)
	@echo "Built $(BINARY) ($(GOOS)/$(GOARCH))"

vet: ## Run go vet across all packages
	go vet ./...

test: vet ## Run vet and race-enabled unit tests
	go test -race ./...

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

check-vps:
	@if [ -z "$(VPS_HOST)" ]; then \
		echo "Error: VPS_HOST is not set."; \
		echo "Copy .deploy.mk.example to .deploy.mk or pass: make deploy VPS_HOST=user@your-vps"; \
		exit 1; \
	fi

setup-vps: check-vps ## One-time VPS setup: create directories and install systemd unit
	@echo "Setting up $(VPS_DIR) and systemd service $(VPS_SERVICE) on $(VPS_HOST)..."
	$(SSH) $(VPS_HOST) 'set -e; RUN_USER="$(VPS_USER)"; [ -n "$$RUN_USER" ] || RUN_USER="$$(id -un)"; $(SUDO) mkdir -p "$(VPS_DIR)/ui" "$(VPS_DIR)/db" "$(VPS_DIR)/data" "$(VPS_DIR)/tls"; $(SUDO) chown -R "$$RUN_USER:$$(id -gn "$$RUN_USER")" "$(VPS_DIR)"'
	@sed -e 's|{{VPS_DIR}}|$(VPS_DIR)|g' -e 's|{{APP_NAME}}|$(APP_NAME)|g' deploy/inkfetch.service | \
		$(SSH) $(VPS_HOST) 'set -e; RUN_USER="$(VPS_USER)"; [ -n "$$RUN_USER" ] || RUN_USER="$$(id -un)"; sed "s|{{VPS_USER}}|$$RUN_USER|g" | $(SUDO) tee /etc/systemd/system/$(VPS_SERVICE).service >/dev/null; $(SUDO) systemctl daemon-reload; $(SUDO) systemctl enable $(VPS_SERVICE)'
	@echo ""
	@echo "Systemd service $(VPS_SERVICE) installed and enabled."
	@echo "Next steps:"
	@echo "  1. Push or create $(VPS_DIR)/.env and $(VPS_DIR)/tls/ (or run: make sync-env sync-tls)"
	@echo "  2. Deploy the app: make deploy"

deploy: test build-linux _upload-and-restart ## Run tests, build Linux binary, upload binary + ui/, and restart VPS service

deploy-fast: build-linux _upload-and-restart ## Build Linux binary and deploy to VPS (skip local tests)

_upload-and-restart: check-vps
	@echo "Deploying $(APP_NAME) to $(VPS_HOST):$(VPS_DIR)..."
	$(SSH) $(VPS_HOST) 'mkdir -p "$(VPS_DIR)/ui" "$(VPS_DIR)/db" "$(VPS_DIR)/data" "$(VPS_DIR)/tls"'
	$(RSYNC) --progress $(BINARY) $(VPS_HOST):$(VPS_DIR)/$(APP_NAME).new
	$(RSYNC) --delete ui/ $(VPS_HOST):$(VPS_DIR)/ui/
	$(SSH) $(VPS_HOST) 'set -e; chmod +x "$(VPS_DIR)/$(APP_NAME).new"; mv -f "$(VPS_DIR)/$(APP_NAME).new" "$(VPS_DIR)/$(APP_NAME)"; $(SUDO) systemctl restart $(VPS_SERVICE); sleep 1; $(SUDO) systemctl is-active --quiet $(VPS_SERVICE)'
	@echo "Deployed and restarted $(VPS_SERVICE) successfully."

sync-env: check-vps ## Upload local .env to VPS (manual — never overwritten by deploy)
	$(RSYNC) .env $(VPS_HOST):$(VPS_DIR)/.env
	@echo "Uploaded .env to $(VPS_HOST):$(VPS_DIR)/.env"

sync-tls: check-vps ## Upload local tls/ certs to VPS (manual — never overwritten by deploy)
	$(RSYNC) tls/ $(VPS_HOST):$(VPS_DIR)/tls/
	@echo "Uploaded tls/ to $(VPS_HOST):$(VPS_DIR)/tls/"

status: check-vps ## Show systemd service status on VPS
	$(SSH) $(VPS_HOST) '$(SUDO) systemctl --no-pager -l status $(VPS_SERVICE)'

logs: check-vps ## Stream live service logs from VPS
	$(SSH) $(VPS_HOST) '$(SUDO) journalctl -u $(VPS_SERVICE) -f -n 100'

restart: check-vps ## Restart InkFetch service on VPS
	$(SSH) $(VPS_HOST) '$(SUDO) systemctl restart $(VPS_SERVICE) && $(SUDO) systemctl is-active $(VPS_SERVICE)'

stop: check-vps ## Stop InkFetch service on VPS
	$(SSH) $(VPS_HOST) '$(SUDO) systemctl stop $(VPS_SERVICE)'

ssh: check-vps ## Open an interactive SSH shell in the remote app directory
	$(SSH) -t $(VPS_HOST) 'cd "$(VPS_DIR)" && exec $$SHELL -l'

backup-db: check-vps ## Download a consistent timestamped SQLite backup from VPS into backups/
	@mkdir -p backups
	@TS=$$(date +%Y%m%d-%H%M%S); \
	REMOTE_TMP="/tmp/$(APP_NAME)-backup-$$TS.db"; \
	LOCAL_OUT="backups/$(APP_NAME)-$$TS.db"; \
	$(SSH) $(VPS_HOST) "set -e; cd '$(VPS_DIR)'; DB_FILE=\$$(sed -n 's/^DB_PATH=//p' .env 2>/dev/null | head -n1 | tr -d '\"'); DB_FILE=\$${DB_FILE:-./db/zenq.db}; if command -v sqlite3 >/dev/null 2>&1; then sqlite3 \"\$$DB_FILE\" \".backup $$REMOTE_TMP\"; else cp \"\$$DB_FILE\" \"$$REMOTE_TMP\"; fi"; \
	$(RSYNC) $(VPS_HOST):"$$REMOTE_TMP" "$$LOCAL_OUT"; \
	$(SSH) $(VPS_HOST) "rm -f $$REMOTE_TMP"; \
	echo "Saved DB backup to $$LOCAL_OUT"
