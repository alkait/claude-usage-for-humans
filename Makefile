BIN     := cuh
GO      ?= go
DOCKER  ?= docker
COMPOSE ?= $(shell $(DOCKER) compose version >/dev/null 2>&1 && echo "$(DOCKER) compose" || echo docker-compose)
export UID := $(shell id -u)
export GID := $(shell id -g)

.PHONY: build test release install image up down restart logs sample serve clean

build:            ## local binary
	$(GO) build -ldflags="-s -w" -o $(BIN) .

test:             ## unit tests
	$(GO) test ./...

release:          ## all platforms into dist/
	./build.sh

install: build    ## copy the binary to ~/.local/bin
	install -m 0755 $(BIN) $(HOME)/.local/bin/$(BIN)

image:            ## container image
	$(DOCKER) build -t cuh:local .

up:               ## CUH_SECRET=... [CUH_PORT=8787] make up
	mkdir -p data
	$(COMPOSE) up -d --build

down:             ## stop and remove the container
	$(DOCKER) rm -f cuh

restart:          ## restart the container, keeping its secret and port
	$(DOCKER) restart cuh

logs:             ## follow the server log
	$(DOCKER) logs -f cuh

sample:           ## show what the running server answers on /now
	@curl -fsS -H "Authorization: Bearer $(CUH_SECRET)" http://localhost:$(or $(CUH_PORT),8787)/now | python3 -m json.tool

serve: build      ## run the server directly, no container (data in ./data)
	./$(BIN) serve --listen :$(or $(CUH_PORT),8787) --data-dir ./data --secret "$(CUH_SECRET)"

clean:
	rm -rf $(BIN) dist
