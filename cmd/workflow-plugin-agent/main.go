// Command workflow-plugin-agent hosts the agent plugin over the workflow
// external-plugin protocol.
//
// ServePluginFull is used (rather than the bare sdk.Serve) so the binary can
// later gain CLI and/or build-hook dispatch without a re-architecture, mirroring
// workflow-plugin-infra/cmd/workflow-plugin-infra. The agent plugin exposes no
// CLI or hooks yet, so the cli and hooks arguments are nil.
package main

import (
	agent "github.com/GoCodeAlone/workflow-plugin-agent"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func main() {
	sdk.ServePluginFull(
		agent.New(),
		nil, // no CLI provider yet
		nil, // no hook handler yet
		sdk.WithBuildVersion(sdk.ResolveBuildVersion(agent.Version)),
	)
}
