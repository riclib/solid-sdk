package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// fakeRunnable is a configurable Runnable for exercising the registry adapter
// without a live NATS server — the registry is pure dispatch/mapping logic.
type fakeRunnable struct {
	typ     string
	configs []contract.ConfigOption
	run     func(emit func(contract.RunnableProgress)) (contract.RunnableResult, error)
}

func (f fakeRunnable) Type() string                         { return f.typ }
func (f fakeRunnable) DisplayName() string                  { return f.typ + " display" }
func (f fakeRunnable) Description() string                  { return f.typ + " description" }
func (f fakeRunnable) ListConfigs() []contract.ConfigOption { return f.configs }
func (f fakeRunnable) Execute(_ context.Context, _ string, emit func(contract.RunnableProgress)) (contract.RunnableResult, error) {
	return f.run(emit)
}

func TestRunnableRegistry_Descriptors_SortedAndComplete(t *testing.T) {
	reg := NewRunnableRegistry(
		fakeRunnable{typ: "zeta", configs: []contract.ConfigOption{{ID: "z/1", Name: "Z One"}}},
		fakeRunnable{typ: "alpha"},
	)
	ds := reg.Descriptors()
	if len(ds) != 2 {
		t.Fatalf("descriptors len = %d, want 2", len(ds))
	}
	// Sorted by Type for byte-stable manifests.
	if ds[0].Type != "alpha" || ds[1].Type != "zeta" {
		t.Fatalf("descriptors not sorted by type: %q, %q", ds[0].Type, ds[1].Type)
	}
	if ds[1].DisplayName != "zeta display" || ds[1].Description != "zeta description" {
		t.Fatalf("descriptor metadata not carried: %+v", ds[1])
	}
	if len(ds[1].ConfigOptions) != 1 || ds[1].ConfigOptions[0].ID != "z/1" {
		t.Fatalf("descriptor config options not carried: %+v", ds[1].ConfigOptions)
	}
	if got := reg.Types(); len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("Types() = %v, want [alpha zeta]", got)
	}
}

func TestRunnableRegistry_Handler_UnknownType(t *testing.T) {
	reg := NewRunnableRegistry()
	res := reg.Handler()(context.Background(),
		contract.RunnableRunRequest{Type: "nope", RunID: "r1"}, func(contract.RunnableProgress) {})
	if res.Status != contract.RunStatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.RunID != "r1" {
		t.Fatalf("run id = %q, want r1 echoed", res.RunID)
	}
	if res.Error == "" {
		t.Fatal("want a non-empty error for an unknown type")
	}
}

func TestRunnableRegistry_Handler_Dispatch(t *testing.T) {
	cases := []struct {
		name       string
		run        func(emit func(contract.RunnableProgress)) (contract.RunnableResult, error)
		wantStatus string
		wantMsg    string
		wantErr    string
		wantEmits  int
	}{
		{
			name: "completed with progress",
			run: func(emit func(contract.RunnableProgress)) (contract.RunnableResult, error) {
				emit(contract.RunnableProgress{Message: "step 1"})
				emit(contract.RunnableProgress{Message: "step 2"})
				return contract.RunnableResult{Message: "done"}, nil
			},
			wantStatus: contract.RunStatusCompleted, wantMsg: "done", wantEmits: 2,
		},
		{
			name: "skipped is honored",
			run: func(emit func(contract.RunnableProgress)) (contract.RunnableResult, error) {
				return contract.RunnableResult{Status: contract.RunStatusSkipped, Message: "nothing to do"}, nil
			},
			wantStatus: contract.RunStatusSkipped, wantMsg: "nothing to do",
		},
		{
			name: "error becomes failed result with message fallback",
			run: func(emit func(contract.RunnableProgress)) (contract.RunnableResult, error) {
				return contract.RunnableResult{}, errors.New("boom")
			},
			wantStatus: contract.RunStatusFailed, wantMsg: "boom", wantErr: "boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRunnableRegistry(fakeRunnable{typ: "job", run: tc.run})
			emits := 0
			res := reg.Handler()(context.Background(),
				contract.RunnableRunRequest{Type: "job", RunID: "run-7"},
				func(contract.RunnableProgress) { emits++ })
			if res.RunID != "run-7" {
				t.Fatalf("run id = %q, want run-7 stamped", res.RunID)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", res.Status, tc.wantStatus)
			}
			if res.Message != tc.wantMsg {
				t.Fatalf("message = %q, want %q", res.Message, tc.wantMsg)
			}
			if res.Error != tc.wantErr {
				t.Fatalf("error = %q, want %q", res.Error, tc.wantErr)
			}
			if emits != tc.wantEmits {
				t.Fatalf("emits = %d, want %d", emits, tc.wantEmits)
			}
		})
	}
}
