package provider

import (
	"context"
	"testing"
)

type mockStatefulProvider struct {
	resetCalled bool
}

func (m *mockStatefulProvider) Name() string { return "mock-stateful" }
func (m *mockStatefulProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return &Response{Content: "ok"}, nil
}
func (m *mockStatefulProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, nil
}
func (m *mockStatefulProvider) AuthModeInfo() AuthModeInfo { return AuthModeInfo{} }
func (m *mockStatefulProvider) ManagesContext() bool        { return true }
func (m *mockStatefulProvider) ResetContext(_ context.Context) error {
	m.resetCalled = true
	return nil
}

type mockStatelessProvider struct{}

func (m *mockStatelessProvider) Name() string { return "mock-stateless" }
func (m *mockStatelessProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return &Response{Content: "ok"}, nil
}
func (m *mockStatelessProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, nil
}
func (m *mockStatelessProvider) AuthModeInfo() AuthModeInfo { return AuthModeInfo{} }

func TestContextStrategy_Detection(t *testing.T) {
	var p Provider = &mockStatefulProvider{}
	cs, ok := p.(ContextStrategy)
	if !ok {
		t.Fatal("expected provider to implement ContextStrategy")
	}
	if !cs.ManagesContext() {
		t.Error("expected ManagesContext=true")
	}
}

func TestContextStrategy_ResetContext(t *testing.T) {
	m := &mockStatefulProvider{}
	if err := m.ResetContext(context.Background()); err != nil {
		t.Fatalf("ResetContext returned error: %v", err)
	}
	if !m.resetCalled {
		t.Error("expected resetCalled=true after ResetContext")
	}
}

func TestContextStrategy_NotImplemented(t *testing.T) {
	var p Provider = &mockStatelessProvider{}
	_, ok := p.(ContextStrategy)
	if ok {
		t.Error("mockStatelessProvider should not implement ContextStrategy")
	}
}
