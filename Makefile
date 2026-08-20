# Kolo is not released yet, so there is no binary to download and the way to run
# it is to build it. That should be one word.

BIN := kolo
PKG := ./cmd/kolo

# Where go install puts it, which is the thing most likely not to be on PATH.
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

# Stamped into the binary, so a host that dials in says which build it is rather
# than "dev". A working tree with changes in it says so.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: install run test vet fmt clean

# The one to use. Puts kolo on PATH from source, so every other command in the
# docs is the command in the docs.
install:
	go install -ldflags '$(LDFLAGS)' $(PKG)
	@case ":$$PATH:" in \
	*":$(GOBIN):"*) \
	  echo; echo "kolo $(VERSION) installed. Lend a directory:"; echo; \
	  echo "    cd ~/work/api && kolo up"; echo ;; \
	*) \
	  echo; echo "kolo $(VERSION) is in $(GOBIN), which is not on your PATH."; \
	  echo "Add this to your shell profile, then open a new terminal:"; echo; \
	  echo "    export PATH=\"$(GOBIN):\$$PATH\""; echo; \
	  echo "Or skip installing altogether: make run"; echo ;; \
	esac

# Start a hub and lend this directory to it, without installing anything.
# ARGS passes flags through: make run ARGS='-addr 127.0.0.1:7300'
run:
	go run -ldflags '$(LDFLAGS)' $(PKG) up $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BIN)
