GOPRIVATE ?= github.com/GoCodeAlone/*

# protoc + protoc-gen-go must be on PATH. The generated *.pb.go carry a
# `source: internal/contracts/<name>.proto` header, so the invocation runs from
# the repo root with --proto_path=. (NOT --proto_path=internal/contracts, which
# would strip the directory prefix from the source provenance line). go_opt
# strips the module prefix so output lands at internal/contracts/<name>.pb.go.
#
# Tooling:
#   protoc        v7.35.0  (brew install protobuf)
#   protoc-gen-go v1.36.11 (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11)
#
# Regenerate after editing any internal/contracts/*.proto:
#   make proto
PROTOC_GO_OPT := module=github.com/GoCodeAlone/workflow-plugin-agent
PROTO_FILES   := $(wildcard internal/contracts/*.proto)

.PHONY: build test lint clean proto

proto:
	protoc --proto_path=. --go_out=. --go_opt=$(PROTOC_GO_OPT) $(PROTO_FILES)

build:
	GOPRIVATE=$(GOPRIVATE) go build ./...

# proto regenerates internal/contracts/*.pb.go from the matching .proto files.
# See the header comment above for the recorded protoc invocation.

test:
	GOPRIVATE=$(GOPRIVATE) go test -race ./...

lint:
	GOPRIVATE=$(GOPRIVATE) go vet ./...

clean:
	go clean ./...
