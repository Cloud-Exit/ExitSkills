GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
LOCAL_DATABASE_URL ?= sqlite://.local/skills.db
ENV_FILE ?= .env
API_DOCS_DIR ?= dist/api-docs
REDOC_VERSION ?= v2.5.3

DOTENV_VARIABLES := DATABASE_URL GITHUB_TOKEN GITHUB_CLIENT_ID GITHUB_CLIENT_SECRET ENCRYPTION_KEY AI_BASE_URL AI_API_KEY AI_MODEL \
	LISTEN_ADDRESS PUBLIC_BASE_URL INDEX_INTERVAL INDEX_ON_START RATE_LIMIT_REQUESTS \
	RATE_LIMIT_WINDOW REQUEST_TIMEOUT GITHUB_API_BASE_URL OFFICIAL_SKILLS_URL LOG_LEVEL \
	INDEX_CONCURRENCY MASTER_TOKEN
$(foreach variable,$(DOTENV_VARIABLES),$(eval _ENV_$(variable) := $($(variable))))
-include $(ENV_FILE)
$(foreach variable,$(DOTENV_VARIABLES),$(if $(_ENV_$(variable)),$(eval override $(variable) := $(_ENV_$(variable)))))
$(foreach variable,$(DOTENV_VARIABLES),$(eval run admin: export $(variable) := $($(variable))))

.PHONY: test test-race vet fmt-check build run admin docs docker-build helm-lint clean

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o bin/admin ./cmd/admin

run:
	mkdir -p .local
	DATABASE_URL="$(LOCAL_DATABASE_URL)" \
	ENCRYPTION_KEY="$${ENCRYPTION_KEY:-exitmesh-local-development-only}" \
	MASTER_TOKEN="$${MASTER_TOKEN:-exitmesh-local-master-token}" \
	INDEX_ON_START=true \
	EXITMESH_LOCAL_DEVELOPMENT=true $(GO) run ./cmd/server

admin:
	mkdir -p .local
	DATABASE_URL="$(LOCAL_DATABASE_URL)" \
	ENCRYPTION_KEY="$${ENCRYPTION_KEY:-exitmesh-local-development-only}" \
	EXITMESH_LOCAL_DEVELOPMENT=true $(GO) run ./cmd/admin --name "$(NAME)" --valid-for "$(or $(VALID_FOR),720h)"

docs:
	$(GO) run ./cmd/generate-api-docs -spec docs/openapi.json -out "$(API_DOCS_DIR)/index.html" -redoc-version "$(REDOC_VERSION)"

docker-build:
	docker build --build-arg VERSION="$(VERSION)" -t exitmesh-skills:$(VERSION) .

helm-lint:
	helm lint deploy/helm/exitmesh-skills --set existingSecret=lint-only

clean:
	rm -rf bin dist
