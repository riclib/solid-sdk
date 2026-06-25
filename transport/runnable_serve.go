package transport

import (
	"context"
	"fmt"
	"sort"

	"github.com/riclib/solid-sdk/contract"
)

// The serve-side convenience over the runnable wire (runnable.go). A solution
// implements the small Runnable interface per runnable type; a RunnableRegistry
// turns a set of them into the two things every runnable-serving solution needs
// from one source of truth:
//
//   - Handler() — the RunnableHandler ServeRunnables drives (trigger → dispatch
//     → progress → terminal result), so the solution never hand-rolls the
//     type-switch + result-mapping plumbing.
//   - Descriptors() — the manifest RunnableDescriptors to announce (Type /
//     DisplayName / Description / ConfigOptions), so the SERVED handler and the
//     ANNOUNCED descriptor can never drift (same registry builds both).
//
// This is the SDK home for what the in-tree jobs.Runnable adapter used to do in
// each fork. The old jobs.Runnable shape
// (Type/DisplayName/Validate/ListConfigs/Execute over a jobs.Progress channel)
// maps onto this one-to-one, minus any v4 import.

// Runnable is the solution-side implementation of one runnable type — the serve
// counterpart of the RunnableDescriptor the solution announces. Execute relays
// progress through emit and returns the terminal result; a returned Go error
// becomes a failed result. RunID is stamped by the registry, so Execute need not
// set it.
type Runnable interface {
	// Type is the wire identifier the platform triggers (RunnableRunRequest.Type).
	Type() string

	// DisplayName is the operator-facing label for the manifest descriptor.
	DisplayName() string

	// Description is the one-line summary the platform's runnable picker renders.
	Description() string

	// ListConfigs are the selectable configs the job-step picker renders — the
	// ConfigOptions of the announced descriptor. The chosen one's ID rides in
	// RunnableRunRequest.ConfigID and reaches Execute as configID.
	ListConfigs() []contract.ConfigOption

	// Execute runs one invocation, calling emit for each progress line, and
	// returns the terminal result. A returned error becomes a failed result;
	// otherwise the returned Status (Completed/Skipped) is honored — an empty
	// Status defaults to Completed. The registry stamps RunID onto the result.
	Execute(ctx context.Context, configID string, emit func(contract.RunnableProgress)) (contract.RunnableResult, error)
}

// RunnableRegistry maps a runnable Type → its Runnable. Build one with
// NewRunnableRegistry / Register, serve it via Handler, and announce it via
// Descriptors. Not safe for concurrent registration; register at construction
// and treat as read-only thereafter (the serve loop is single-in-flight).
type RunnableRegistry struct {
	byType map[string]Runnable
}

// NewRunnableRegistry builds a registry over the given runnables (keyed by Type).
func NewRunnableRegistry(runnables ...Runnable) *RunnableRegistry {
	reg := &RunnableRegistry{byType: make(map[string]Runnable, len(runnables))}
	for _, r := range runnables {
		reg.Register(r)
	}
	return reg
}

// Register adds r under its Type. A duplicate Type is last-write-wins.
func (reg *RunnableRegistry) Register(r Runnable) { reg.byType[r.Type()] = r }

// Types returns the registered runnable Types, sorted, for logging at serve start.
func (reg *RunnableRegistry) Types() []string {
	out := make([]string, 0, len(reg.byType))
	for t := range reg.byType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Descriptors builds the manifest RunnableDescriptors for every registered
// runnable — the announce-side wire form. Sorted by Type so a manifest re-publish
// is byte-stable. Feed these into SolutionPublish.Runnables.
func (reg *RunnableRegistry) Descriptors() []contract.RunnableDescriptor {
	out := make([]contract.RunnableDescriptor, 0, len(reg.byType))
	for _, r := range reg.byType {
		out = append(out, contract.RunnableDescriptor{
			Type:          r.Type(),
			DisplayName:   r.DisplayName(),
			Description:   r.Description(),
			ConfigOptions: r.ListConfigs(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Handler adapts the registry into the RunnableHandler ServeRunnables drives:
// dispatch req.Type → the registered Runnable, run its Execute relaying progress
// through emit, and map the outcome onto the terminal RunnableResult. An unknown
// Type fails as DATA (a failed result, not a panic) so the trigger is acked and
// not redelivered forever.
func (reg *RunnableRegistry) Handler() RunnableHandler {
	return func(ctx context.Context, req contract.RunnableRunRequest, emit func(contract.RunnableProgress)) contract.RunnableResult {
		r, ok := reg.byType[req.Type]
		if !ok {
			return contract.RunnableResult{
				RunID:  req.RunID,
				Status: contract.RunStatusFailed,
				Error:  fmt.Sprintf("no runnable registered for type %q", req.Type),
			}
		}
		res, err := r.Execute(ctx, req.ConfigID, emit)
		res.RunID = req.RunID
		switch {
		case err != nil:
			res.Status = contract.RunStatusFailed
			res.Error = err.Error()
			if res.Message == "" {
				res.Message = err.Error()
			}
		case res.Status == "":
			res.Status = contract.RunStatusCompleted
		}
		return res
	}
}
