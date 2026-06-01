// Command workflow-plugin-agent hosts the agent plugin over the workflow
// external-plugin protocol.
package main

import (
	agent "github.com/GoCodeAlone/workflow-plugin-agent"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func main() {
	sdk.Serve(agent.New(), sdk.WithBuildVersion(sdk.ResolveBuildVersion(agent.Version)))
}
