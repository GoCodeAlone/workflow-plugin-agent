package executor

import (
	"context"
	"testing"
)

func TestNullTrustEvaluator(t *testing.T) {
	te := &NullTrustEvaluator{}
	if te.Evaluate(context.Background(), "file_write", nil) != ActionAllow {
		t.Error("NullTrustEvaluator should always allow")
	}
	if te.EvaluateCommand("rm -rf /") != ActionAllow {
		t.Error("NullTrustEvaluator should always allow commands")
	}
}

func TestDenyAllTrustEvaluator(t *testing.T) {
	te := &DenyAllTrustEvaluator{}
	if te.Evaluate(context.Background(), "file_write", nil) != ActionDeny {
		t.Error("DenyAllTrustEvaluator should always deny")
	}
	if te.EvaluateCommand("echo hello") != ActionDeny {
		t.Error("DenyAllTrustEvaluator should always deny commands")
	}
	if te.EvaluatePath("/tmp/file") != ActionDeny {
		t.Error("DenyAllTrustEvaluator should always deny paths")
	}
}

type mockTrustEvaluator struct {
	toolAction Action
	cmdAction  Action
}

func (m *mockTrustEvaluator) Evaluate(_ context.Context, _ string, _ map[string]any) Action {
	return m.toolAction
}
func (m *mockTrustEvaluator) EvaluateCommand(_ string) Action { return m.cmdAction }
func (m *mockTrustEvaluator) EvaluatePath(_ string) Action    { return ActionAllow }

func TestTrustEvaluatorInterface(t *testing.T) {
	var te TrustEvaluator = &mockTrustEvaluator{toolAction: ActionDeny}
	if te.Evaluate(context.Background(), "bash", nil) != ActionDeny {
		t.Error("mock should deny")
	}
}
