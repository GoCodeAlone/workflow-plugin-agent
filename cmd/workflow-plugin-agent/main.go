// Command workflow-plugin-agent hosts the agent plugin over the workflow
// external-plugin protocol.
//
// As of Phase 2b (ADR 0053 — orchestrator unconditional fold-in), the binary
// serves the UNION of the agent plugin's 4 step types and the orchestrator's 3
// stateless step types (step.lsp_diagnose, step.self_improve_validate,
// step.self_improve_diff) = 7, all under the AGENT plugin name+version. The
// fold-in is ALWAYS-ON (no flag): the orchestrator subset is served in-process
// via orchestrator.NewUnionAdapter() (orchestrator/grpc_adapter.go), which
// delegates Manifest/Module/Typed* to the embedded agent plugin and dispatches
// CreateStep across both factory maps. See
// decisions/0053-orchestrator-unconditional-foldin.md for the rationale + the
// build-tag fallback that was rejected.
//
// ServePluginFull is used (rather than the bare sdk.Serve) so the binary can
// later gain CLI and/or build-hook dispatch without a re-architecture, mirroring
// workflow-plugin-infra/cmd/workflow-plugin-infra. The union adapter exposes no
// CLI or hooks yet, so the cli and hooks arguments are nil.
package main

import (
	agent "github.com/GoCodeAlone/workflow-plugin-agent"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func main() {
	sdk.ServePluginFull(
		orchestrator.NewUnionAdapter(),
		nil, // no CLI provider yet
		nil, // no hook handler yet
		sdk.WithBuildVersion(sdk.ResolveBuildVersion(agent.Version)),
	)
}
