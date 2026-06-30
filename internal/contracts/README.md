# internal/contracts

Protobuf message contracts for the plugin's strict-contracts descriptors. Each
`*.proto` has a generated `*.pb.go` (Go `package contracts`) checked into the
tree; the descriptors in `../../plugin.contracts.json` reference these messages
by fully-qualified name (`workflow.plugins.<area>.v1.<Message>`).

## Files

- `agent.proto` / `agent.pb.go` — `workflow.plugins.agent.v1` (agent.provider +
  the 4 agent step types).
- `orchestrator.proto` / `orchestrator.pb.go` — `workflow.plugins.orchestrator.v1`
  (the 3 stateless orchestrator step types: `step.lsp_diagnose`,
  `step.self_improve_validate`, `step.self_improve_diff`).

## Regenerating

The codegen is also recorded as a `make proto` target in the repo Makefile. Run
from the repo root after installing the toolchain:

```sh
brew install protobuf                                        # protoc v7.35.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
make proto
```

The exact invocation (run from the repo root, NOT from this directory — the
generated header's `source:` line preserves the `internal/contracts/` prefix
only when `--proto_path=.` points at the repo root):

```sh
protoc --proto_path=. \
       --go_out=. \
       --go_opt=module=github.com/GoCodeAlone/workflow-plugin-agent \
       internal/contracts/*.proto
```

The `--go_opt=module=...` flag strips the module prefix so each `*.pb.go` lands
next to its `*.proto` here. Do NOT edit generated files by hand.
