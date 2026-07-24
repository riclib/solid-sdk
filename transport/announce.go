// Package transport carries the thin NATS/JetStream helpers over the contract
// wire types: the capability announcement (KV tree) and tool serving/calling
// (request-reply). It takes the standard nats.go handles (*nats.Conn,
// jetstream.JetStream/KeyValue) so it composes with whatever the platform
// already runs — the SDK adds no connection lifecycle of its own.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
)

// MaxArtifactSize is the per-leaf size guard. NATS's default max_payload is
// 1 MB and a KV value is one stream message; we cap a leaf below that with
// headroom for KV/stream overhead.
//
// In practice the cap is a TRIPWIRE for a malformed artifact, never a real
// quota: a skill/prompt goes straight into an LLM context, so it is broken at a
// few tens of KB — a megabyte skill is a context-breaker, not a storage case; a
// dashboard is YAML+SQL (tens of KB) unless someone inlined a base64 image; a
// workflow is ~100 B/step. So an artifact over this cap means a broken artifact
// to fix, NOT a blob to offload. Genuine binary blobs (documents/attachments)
// are a separate, future artifact kind over the NATS object store — not an
// escape valve for these declarative leaves.
const MaxArtifactSize = 900 * 1024

// SolutionPublish is the input to PublishSolution — the solution's core
// metadata plus its artifact leaves. PublishSolution writes each artifact as
// its own KV leaf and the manifest (index) last, so the announce never folds
// big bodies into one oversize descriptor.
type SolutionPublish struct {
	Name         string
	DisplayName  string
	Description  string
	Icon         string
	SystemPrompt string
	Version      string

	Tools       []contract.ToolDescriptor
	Skills      []contract.SkillArtifact
	Prompts     []contract.PromptArtifact
	Workflows   []contract.WorkflowArtifact
	Dashboards  []contract.DashboardArtifact
	Catalogs    []contract.CatalogArtifact
	Projections []contract.ProjectionArtifact
	Runnables   []contract.RunnableDescriptor
	Jobs        []contract.JobArtifact
	Lakes       []contract.LakeArtifact

	// Partner is the optional commercial identity of the organization shipping
	// the solution — copied verbatim into the announced manifest's Partner
	// block. Empty = no partner panel. The logo bytes are published separately
	// to the object store (see PutAsset); Partner.LogoRef carries only the key.
	Partner contract.Partner

	// Fires declares the workflows this solution fires over the fire wire — copied
	// verbatim into the announced manifest's Fires block (a capability
	// declaration; see contract.FireDescriptor). Empty = the solution fires
	// nothing.
	Fires []contract.FireDescriptor
}

// EnsureSolutionsBucket creates-or-gets the solutions announce bucket. Mirrors
// v4's createOrGetKVBucket pattern (FileStorage, get-then-create). Idempotent.
func EnsureSolutionsBucket(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	if kv, err := js.KeyValue(ctx, contract.SolutionsBucket); err == nil {
		return kv, nil
	}
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      contract.SolutionsBucket,
		Description: "Partner solution manifests + artifact leaves (capability announcement)",
		Storage:     jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure solutions bucket: %w", err)
	}
	return kv, nil
}

// PublishSolution announces a solution as a KV tree: it writes every artifact
// leaf first, then the manifest (the commit point) last with a bumped revision.
// Commit-last guarantees a watcher that reacts to the manifest finds every
// referenced leaf already present. Re-publishing is the only update path — any
// change (even a content edit to one leaf) re-runs this, bumping the revision
// so `*.manifest` watchers observe it.
//
// Leaves removed since the last publish are purged best-effort so a watcher
// never resolves a stale leaf the new manifest no longer references.
func PublishSolution(ctx context.Context, kv jetstream.KeyValue, p SolutionPublish) error {
	if p.Name == "" {
		return fmt.Errorf("publish: solution has no name")
	}

	// Build the index + write leaves (commit-last: leaves before manifest).
	var index []contract.ArtifactRef
	for _, t := range p.Tools {
		if t.Name == "" {
			return fmt.Errorf("publish %q: tool with empty name", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactTool, t.Name)
		if err := putLeaf(ctx, kv, key, t); err != nil {
			return fmt.Errorf("publish %q: tool %q: %w", p.Name, t.Name, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactTool, ID: t.Name})
	}
	for _, sk := range p.Skills {
		if sk.ID == "" {
			return fmt.Errorf("publish %q: skill with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactSkill, sk.ID)
		if err := putLeaf(ctx, kv, key, sk); err != nil {
			return fmt.Errorf("publish %q: skill %q: %w", p.Name, sk.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactSkill, ID: sk.ID})
	}
	for _, pr := range p.Prompts {
		if pr.ID == "" {
			return fmt.Errorf("publish %q: prompt with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactPrompt, pr.ID)
		if err := putLeaf(ctx, kv, key, pr); err != nil {
			return fmt.Errorf("publish %q: prompt %q: %w", p.Name, pr.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactPrompt, ID: pr.ID})
	}
	for _, wf := range p.Workflows {
		if wf.ID == "" {
			return fmt.Errorf("publish %q: workflow with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactWorkflow, wf.ID)
		if err := putLeaf(ctx, kv, key, wf); err != nil {
			return fmt.Errorf("publish %q: workflow %q: %w", p.Name, wf.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactWorkflow, ID: wf.ID})
	}
	for _, db := range p.Dashboards {
		if db.ID == "" {
			return fmt.Errorf("publish %q: dashboard with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactDashboard, db.ID)
		if err := putLeaf(ctx, kv, key, db); err != nil {
			return fmt.Errorf("publish %q: dashboard %q: %w", p.Name, db.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactDashboard, ID: db.ID})
	}
	for _, cat := range p.Catalogs {
		if cat.ID == "" {
			return fmt.Errorf("publish %q: catalog with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactCatalog, cat.ID)
		if err := putLeaf(ctx, kv, key, cat); err != nil {
			return fmt.Errorf("publish %q: catalog %q: %w", p.Name, cat.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactCatalog, ID: cat.ID})
	}
	for _, pj := range p.Projections {
		if pj.ID == "" {
			return fmt.Errorf("publish %q: projection with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactProjection, pj.ID)
		if err := putLeaf(ctx, kv, key, pj); err != nil {
			return fmt.Errorf("publish %q: projection %q: %w", p.Name, pj.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactProjection, ID: pj.ID})
	}
	for _, rn := range p.Runnables {
		if rn.Type == "" {
			return fmt.Errorf("publish %q: runnable with empty type", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactRunnable, rn.Type)
		if err := putLeaf(ctx, kv, key, rn); err != nil {
			return fmt.Errorf("publish %q: runnable %q: %w", p.Name, rn.Type, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactRunnable, ID: rn.Type})
	}
	for _, j := range p.Jobs {
		if j.ID == "" {
			return fmt.Errorf("publish %q: job with empty id", p.Name)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactJob, j.ID)
		if err := putLeaf(ctx, kv, key, j); err != nil {
			return fmt.Errorf("publish %q: job %q: %w", p.Name, j.ID, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactJob, ID: j.ID})
	}
	for _, lk := range p.Lakes {
		// Lakes are validated at publish (partner-side fail-fast): a bad
		// declaration fails HERE with a field-level error instead of greying
		// out platform-side. The platform re-validates before materializing.
		if err := lk.Validate(); err != nil {
			return fmt.Errorf("publish %q: lake: %w", p.Name, err)
		}
		key := contract.ArtifactKey(p.Name, contract.ArtifactLake, lk.Name)
		if err := putLeaf(ctx, kv, key, lk); err != nil {
			return fmt.Errorf("publish %q: lake %q: %w", p.Name, lk.Name, err)
		}
		index = append(index, contract.ArtifactRef{Kind: contract.ArtifactLake, ID: lk.Name})
	}

	// Purge leaves the previous publish had that this one drops.
	if err := purgeStaleLeaves(ctx, kv, p.Name, index); err != nil {
		return fmt.Errorf("publish %q: purge stale leaves: %w", p.Name, err)
	}

	// Commit: rewrite the manifest last, with a bumped revision.
	manifest := contract.SolutionManifest{
		Name:         p.Name,
		DisplayName:  p.DisplayName,
		Description:  p.Description,
		Icon:         p.Icon,
		SystemPrompt: p.SystemPrompt,
		Version:      p.Version,
		Revision:     nextRevision(ctx, kv, p.Name),
		Artifacts:    index,
		Partner:      p.Partner,
		Fires:        p.Fires,
	}
	if err := putLeaf(ctx, kv, contract.ManifestKey(p.Name), manifest); err != nil {
		return fmt.Errorf("publish %q: manifest: %w", p.Name, err)
	}
	return nil
}

// WatchSolutions invokes onPut with the ASSEMBLED solution (manifest + resolved
// leaves) for every current and future solution, and onDelete (if non-nil) when
// a solution's manifest is removed (the grey-out signal). It watches only
// `*.manifest`, so leaf churn alone is invisible — by design, since every
// publish rewrites the manifest. Returns once the initial replay is delivered;
// the watch continues in a goroutine until ctx is cancelled.
//
// A manifest whose leaves fail to resolve (a buggy/racing publisher) is skipped,
// not fatal — announce-time validation is the platform's job before it acts,
// never a watcher panic.
func WatchSolutions(ctx context.Context, kv jetstream.KeyValue, onPut func(contract.Solution), onDelete func(name string)) error {
	w, err := kv.Watch(ctx, contract.ManifestWatchFilter())
	if err != nil {
		return fmt.Errorf("watch solutions: %w", err)
	}
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-w.Updates():
				if e == nil {
					continue // nil marks end of initial replay
				}
				switch e.Operation() {
				case jetstream.KeyValuePut:
					var m contract.SolutionManifest
					if err := json.Unmarshal(e.Value(), &m); err != nil {
						continue
					}
					sol, err := assemble(ctx, kv, m)
					if err != nil {
						continue // unresolved leaf — skip, wait for next publish
					}
					if onPut != nil {
						onPut(sol)
					}
				case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
					if onDelete != nil {
						onDelete(solutionFromManifestKey(e.Key()))
					}
				}
			}
		}
	}()
	return nil
}

// assemble resolves a manifest's leaf refs into a full Solution.
func assemble(ctx context.Context, kv jetstream.KeyValue, m contract.SolutionManifest) (contract.Solution, error) {
	sol := contract.Solution{Manifest: m}
	for _, ref := range m.Artifacts {
		key := contract.ArtifactKey(m.Name, ref.Kind, ref.ID)
		entry, err := kv.Get(ctx, key)
		if err != nil {
			return contract.Solution{}, fmt.Errorf("resolve %s: %w", key, err)
		}
		switch ref.Kind {
		case contract.ArtifactTool:
			var t contract.ToolDescriptor
			if err := json.Unmarshal(entry.Value(), &t); err != nil {
				return contract.Solution{}, fmt.Errorf("decode tool %s: %w", key, err)
			}
			sol.Tools = append(sol.Tools, t)
		case contract.ArtifactSkill:
			var sk contract.SkillArtifact
			if err := json.Unmarshal(entry.Value(), &sk); err != nil {
				return contract.Solution{}, fmt.Errorf("decode skill %s: %w", key, err)
			}
			sol.Skills = append(sol.Skills, sk)
		case contract.ArtifactPrompt:
			var pr contract.PromptArtifact
			if err := json.Unmarshal(entry.Value(), &pr); err != nil {
				return contract.Solution{}, fmt.Errorf("decode prompt %s: %w", key, err)
			}
			sol.Prompts = append(sol.Prompts, pr)
		case contract.ArtifactWorkflow:
			var wf contract.WorkflowArtifact
			if err := json.Unmarshal(entry.Value(), &wf); err != nil {
				return contract.Solution{}, fmt.Errorf("decode workflow %s: %w", key, err)
			}
			sol.Workflows = append(sol.Workflows, wf)
		case contract.ArtifactDashboard:
			var db contract.DashboardArtifact
			if err := json.Unmarshal(entry.Value(), &db); err != nil {
				return contract.Solution{}, fmt.Errorf("decode dashboard %s: %w", key, err)
			}
			sol.Dashboards = append(sol.Dashboards, db)
		case contract.ArtifactCatalog:
			var cat contract.CatalogArtifact
			if err := json.Unmarshal(entry.Value(), &cat); err != nil {
				return contract.Solution{}, fmt.Errorf("decode catalog %s: %w", key, err)
			}
			sol.Catalogs = append(sol.Catalogs, cat)
		case contract.ArtifactProjection:
			var pj contract.ProjectionArtifact
			if err := json.Unmarshal(entry.Value(), &pj); err != nil {
				return contract.Solution{}, fmt.Errorf("decode projection %s: %w", key, err)
			}
			sol.Projections = append(sol.Projections, pj)
		case contract.ArtifactRunnable:
			var rn contract.RunnableDescriptor
			if err := json.Unmarshal(entry.Value(), &rn); err != nil {
				return contract.Solution{}, fmt.Errorf("decode runnable %s: %w", key, err)
			}
			sol.Runnables = append(sol.Runnables, rn)
		case contract.ArtifactJob:
			var j contract.JobArtifact
			if err := json.Unmarshal(entry.Value(), &j); err != nil {
				return contract.Solution{}, fmt.Errorf("decode job %s: %w", key, err)
			}
			sol.Jobs = append(sol.Jobs, j)
		case contract.ArtifactLake:
			var lk contract.LakeArtifact
			if err := json.Unmarshal(entry.Value(), &lk); err != nil {
				return contract.Solution{}, fmt.Errorf("decode lake %s: %w", key, err)
			}
			sol.Lakes = append(sol.Lakes, lk)
		default:
			// Unknown future leaf kinds resolve here when their wires land.
		}
	}
	return sol, nil
}

// putLeaf marshals v and Puts it at key, enforcing the per-leaf size guard.
func putLeaf(ctx context.Context, kv jetstream.KeyValue, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	if len(b) > MaxArtifactSize {
		return fmt.Errorf("artifact %s is %d bytes, over the %d-byte KV leaf cap — this is a malformed artifact to fix (a context-breaking skill/prompt, or a dashboard with an inlined blob), not a storage problem; genuine binary blobs belong in a separate document artifact over the object store", key, len(b), MaxArtifactSize)
	}
	if _, err := kv.Put(ctx, key, b); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// nextRevision reads the current manifest revision and returns +1 (1 if absent).
func nextRevision(ctx context.Context, kv jetstream.KeyValue, solution string) uint64 {
	entry, err := kv.Get(ctx, contract.ManifestKey(solution))
	if err != nil {
		return 1
	}
	var m contract.SolutionManifest
	if err := json.Unmarshal(entry.Value(), &m); err != nil {
		return 1
	}
	return m.Revision + 1
}

// purgeStaleLeaves deletes leaf keys present in the bucket for this solution but
// absent from the new index (artifacts dropped since the last publish).
func purgeStaleLeaves(ctx context.Context, kv jetstream.KeyValue, solution string, keep []contract.ArtifactRef) error {
	keys, err := kv.ListKeys(ctx)
	if err != nil {
		return err
	}
	defer keys.Stop()
	wanted := make(map[string]struct{}, len(keep))
	for _, ref := range keep {
		wanted[contract.ArtifactKey(solution, ref.Kind, ref.ID)] = struct{}{}
	}
	prefix := solution + "."
	for key := range keys.Keys() {
		if !strings.HasPrefix(key, prefix) || key == contract.ManifestKey(solution) {
			continue // not ours, or the manifest itself (rewritten, not purged)
		}
		if _, ok := wanted[key]; ok {
			continue
		}
		if err := kv.Purge(ctx, key); err != nil {
			return fmt.Errorf("purge %s: %w", key, err)
		}
	}
	return nil
}

// solutionFromManifestKey extracts `<name>` from `<name>.manifest`.
func solutionFromManifestKey(key string) string {
	return strings.TrimSuffix(key, ".manifest")
}
