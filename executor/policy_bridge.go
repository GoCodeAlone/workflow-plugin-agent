package executor

import (
	"context"

	"github.com/GoCodeAlone/workflow-plugin-agent/policy"
)

// PolicyTrustBridge adapts policy.TrustEngine to executor.TrustEvaluator.
// It provides a compile-time-safe mapping between policy.Action and executor.Action.
type PolicyTrustBridge struct {
	te *policy.TrustEngine
}

// NewPolicyTrustBridge creates a TrustEvaluator backed by a policy.TrustEngine.
func NewPolicyTrustBridge(te *policy.TrustEngine) TrustEvaluator {
	return &PolicyTrustBridge{te: te}
}

func (b *PolicyTrustBridge) Evaluate(ctx context.Context, toolName string, args map[string]any) Action {
	return policyActionToExecutor(b.te.Evaluate(ctx, toolName, args))
}

func (b *PolicyTrustBridge) EvaluateCommand(cmd string) Action {
	return policyActionToExecutor(b.te.EvaluateCommand(cmd))
}

func (b *PolicyTrustBridge) EvaluatePath(path string) Action {
	return policyActionToExecutor(b.te.EvaluatePath(path))
}

// policyActionToExecutor converts a policy.Action to an executor.Action.
func policyActionToExecutor(a policy.Action) Action {
	switch a {
	case policy.Allow:
		return ActionAllow
	case policy.Deny:
		return ActionDeny
	case policy.Ask:
		return ActionAsk
	default:
		return ActionDeny
	}
}
