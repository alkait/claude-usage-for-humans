BIN     := claude-usage
GO      ?= go
DOCKER  ?= docker
COMPOSE ?= $(shell $(DOCKER) compose version >/dev/null 2>&1 && echo "$(DOCKER) compose" || echo docker-compose)
PORT    ?= $(shell sed -n 's/^CLAUDE_USAGE_PORT=//p' .env 2>/dev/null)
SECRET  ?= $(shell sed -n 's/^CLAUDE_USAGE_SECRET=//p' .env 2>/dev/null)
export UID := $(shell id -u)
export GID := $(shell id -g)

.PHONY: build test release install image up down logs restart sample serve refresh clean

build:            ## local binary
	$(GO) build -ldflags="-s -w" -o $(BIN) .

test:             ## unit tests
	$(GO) test ./...

release:          ## all platforms into dist/
	./build.sh

install: build    ## copy the binary to ~/.local/bin
	install -m 0755 $(BIN) $(HOME)/.local/bin/$(BIN)

image:            ## container image
	$(DOCKER) build -t claude-usage:local .

up:               ## build and start the server in a container
	@test -f .env || { echo "copy .env.example to .env first"; exit 1; }
	mkdir -p data
	$(COMPOSE) up -d --build

down:             ## stop the container
	$(COMPOSE) down

restart: down up

logs:             ## follow the server log
	$(COMPOSE) logs -f

sample:           ## show what the running server answers on /now
	@curl -fsS -H "Authorization: Bearer $(SECRET)" http://localhost:$(or $(PORT),8787)/now | python3 -m json.tool

serve: build      ## run the server directly, no container (data in ./data)
	./$(BIN) serve --data-dir ./data --secret "$(SECRET)"

refresh: build    ## force a token refresh against the real endpoint
	./$(BIN) auth refresh --force

clean:
	rm -rf $(BIN) dist
