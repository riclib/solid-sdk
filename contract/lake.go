package contract

import (
	"fmt"
	"sort"
	"strings"
)

// LakeArtifact is the leaf payload for an ArtifactLake — the lake a solution
// declares (S-1874, "demos over the lake"). Named from the SDK user's point
// of view: the partner declares A LAKE — an append-only, signed record plus
// the projections that serve it; "tenant" below is the platform's plumbing
// word for a lake's slot in the estate. It is the
// one artifact that declares a DATA plane rather than control-plane content:
// on operator approval the platform materializes it into a lake tenant (an
// append-only, signed, immutable record), wsstore projections bound per
// workspace from the solution-binding roster, bind-time views, and (when
// Ingest is declared) a generic FILE-door ingest runnable plus a seeded,
// DISABLED job.
//
// Unlike every other data artifact, the declaration is TYPED rather than an
// opaque Body blob: the platform enforces these fields at materialization
// (reserved names, projection SQL guardrails, explicit retention), and a typed
// wire is the only way a partner gets those errors at publish time instead of
// as a greyed-out solution. Validation here is the partner-side fail-fast;
// the platform re-validates independently before acting (announce-time
// validation remains the platform's job — this is a courtesy, not the gate).
//
// Nothing here is behavior: Validate is pure structural checking over the
// declaration (stdlib only), consistent with contract/ being pure data.
type LakeArtifact struct {
	// Name is BOTH the artifact leaf id (`<solution>.lake.<name>`) and the
	// platform-side lake-tenant identifier (it also becomes the served DuckDB
	// schema). Validated: lowercase identifier ([a-z][a-z0-9_]*), refused when
	// reserved (the in-tree tenants — "conversations", "metrics", "audit",
	// solidmon's "adf_ops"/"cdhkpi" — DuckDB's own schemas, anything prefixed
	// "solid", and any `_admin` suffix, which is the admin-schema namespace).
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // the solution that ships it

	// Streams declares the wire shape per stream: name + ordered typed
	// columns. The physical landing order is fixed by the lake — gen (the
	// reserved arrival column), the declared columns in this order, payload,
	// then residual when enabled — so the declared order IS the contract.
	Streams []StreamDecl `json:"streams"`

	// Projections are the wsstore projections the platform binds per
	// workspace (and, where Unscoped, once into the admin engine under a
	// distinct schema).
	Projections []ProjectionDecl `json:"projections,omitempty"`

	// Views are bind-time CREATE OR REPLACE VIEW statements and create-only
	// seed tables (the billing/signals pattern), applied through the
	// platform's engine funnel at bind time.
	Views []ViewDecl `json:"views,omitempty"`

	// Ingests materialize the generic FILE-door ingest: one runnable + one
	// seeded DISABLED job PER entry (the operator enables — the S-1852
	// convention), one entry per stream that lands from files. At least one
	// is REQUIRED in v1: the FILE door is a lake's only write door, and the
	// platform's lake refuses a tenant with no sources. (Plural since the
	// first real consumer: a demo with three streams needs three doors.)
	Ingests []IngestDecl `json:"ingests,omitempty"`

	// Retention is REQUIRED and always explicit: "forever" must be declared,
	// never defaulted (the full-mirror ruling is per-system). Demos declare
	// {Class: "window", Days: 90}.
	Retention RetentionDecl `json:"retention"`

	// Binding names the roster rule: which workspaces the platform binds the
	// tenant's projections into. v1's only value is LakeBindingSolution
	// ("solution"): the roster is the workspaces bound to the announcing
	// solution (the S-1864 rule).
	Binding string `json:"binding"`

	// ScopeLabels declares the NON-workspace labels this lake's projections
	// may bind on (S-2212). Declaring one here is what makes it usable as a
	// ProjectionDecl.ScopeLabel; the label values a workspace claims are
	// platform CONFIG (the workspace's label scope), resolved at BIND time —
	// never stamped on a landed row. See ScopeLabelDecl.
	ScopeLabels []ScopeLabelDecl `json:"scope_labels,omitempty"`
}

// ScopeLabelDecl declares one non-workspace scoping label this lake binds on
// — the label-scoped binding contract (S-2212, the port of the in-tree
// `servicenow_inc` behavior).
//
// The mechanics: a stream carries the label as an ordinary declared column
// (`team`, `region`, …) and lists it in StreamDecl.Labels; a projection names
// it as its ScopeLabel; each workspace's label scope (platform config) says
// which VALUES of that label it claims; the platform resolves config → values
// → predicate AT BIND TIME. Three consequences follow, and they are the whole
// reason binding is late rather than land-time:
//
//   - A reorg is a CONFIG CHANGE, not a data migration. Move a team to another
//     workspace by editing the claim; the next bind resolves the new owner.
//   - HISTORY FOLLOWS. Because the rows carry the label and never an owner,
//     the moved team's whole history moves with it — the new owner inherits
//     the operational record and the precedent corpus, without rewriting a
//     single landed byte.
//   - The record stays immutable. Land-time stamping was REJECTED for exactly
//     this: it would bake today's org chart into an append-only record and put
//     a config concern in the landing layer.
//
// # The exclusivity law
//
// Exclusive marks the label as OWNED: a value belongs to exactly ONE workspace
// per lake. Set it whenever visibility implies ownership — the case a
// generative workflow creates. When a landed row OPENS something (a case, an
// investigation, a notification) in every workspace that can see it, two
// workspaces claiming `team: payments` do not "share a view", they each open
// their own case for the same incident and duplicate the work. That is the
// doctrine-family rule (one writing solution per store) applied to labels.
//
// The SDK cannot check ownership: the claims live in platform config, not in
// the artifact. What it CAN do is make the requirement explicit on the wire
// (this field) and ship the checker both sides run —
// ValidateExclusiveClaims — so the platform's loud refusal at bind/approval
// and any partner-side preflight report the identical conflict.
//
// The unclaimed edge is the other half of the law and belongs to the platform:
// a row whose label value NO workspace claims binds nowhere and generates
// nothing. That must surface as an unowned count on the platform admin screen
// — never a silent gap. The platform reads it from the lake's own tenant
// surface (which sees every landed row regardless of claims), so it needs no
// declaration here.
type ScopeLabelDecl struct {
	// Name is the label: a declared column on at least one of this lake's
	// streams, listed in that stream's Labels. `workspace` is refused — it is
	// the reserved identity label and needs no declaration (see
	// WorkspaceLabel).
	Name string `json:"name"`

	// Description documents what the label means and where its values come
	// from — read by the operator who configures the claims, so write it for
	// them ("ServiceNow assignment group; values are the group's short name").
	Description string `json:"description,omitempty"`

	// Exclusive asserts the ownership law above: each value of this label is
	// claimed by at most one workspace. Validated loudly by the platform at
	// bind/approval; ValidateExclusiveClaims is the shared implementation.
	Exclusive bool `json:"exclusive,omitempty"`
}

// Provenance classifies where a stream's rows come from, which decides ONE
// thing: whether the row may carry a `workspace` column (S-2212).
//
// Source and product data — incidents, advisories, scores — are NEVER
// workspace-stamped. They carry their domain labels (`team`) and the org
// mapping lives only in config, resolved at bind (see ScopeLabelDecl). A
// workspace column on such a row is a config decision frozen into an immutable
// record, and it is what makes a reorg a migration.
//
// Execution exhaust — case rows, invocation records, run ledgers — IS stamped
// with the workspace at open, because there the workspace is a FACT ABOUT WHAT
// HAPPENED: this ran under that workspace's config, routes and engine. It stays
// truthfully stamped forever, including after a reorg moves the team elsewhere
// (the moved team's source/product history follows the label; the old case
// exhaust does not, and should not — it records where it executed). This is the
// conversations-tenant precedent, which stamps for the same reason.
//
// The field is OPTIONAL and its zero value declares nothing: a stream that
// omits it is validated exactly as before.
type Provenance string

const (
	// ProvenanceSource is data ingested from a system of record (incidents,
	// tickets, telemetry). Must not declare a `workspace` column.
	ProvenanceSource Provenance = "source"
	// ProvenanceProduct is data the solution derives from source data
	// (advisories, scores, classifications). Must not declare a `workspace`
	// column.
	ProvenanceProduct Provenance = "product"
	// ProvenanceExhaust is the record of execution (cases opened, invocations,
	// deliveries). MUST declare a `workspace` column — the workspace it ran
	// under is a fact about the row.
	ProvenanceExhaust Provenance = "exhaust"
)

// StreamDecl is one lake stream: name plus ordered typed columns. The lake
// lands one row per envelope record: reserved `gen` first, then these columns
// in declared order, then `payload` (the raw record) and optionally
// `residual` (unpromoted remainder). `gen`, `payload` and `residual` are
// reserved and cannot be declared.
type StreamDecl struct {
	Name string `json:"name"`

	// Columns are the promoted envelope columns, in landing order. Exactly
	// one column must carry Role "time" (the event-time column driving
	// slices, drains and retention).
	Columns []ColumnDecl `json:"columns"`

	// Labels name the declared columns that act as scoping labels for
	// projection binds. `workspace` is the reserved scoping label: a stream
	// feeding a projection bound on workspace IDENTITY (no ScopeLabel) must
	// declare a `workspace` column and list it here. A stream feeding a
	// LABEL-SCOPED projection lists that label instead (see ScopeLabelDecl) —
	// the fail-closed rule is the same either way: a projection whose stream
	// does not EMIT the label it is scoped on binds to nothing.
	Labels []string `json:"labels,omitempty"`

	// Provenance classifies the rows (source / product / exhaust) and decides
	// whether a `workspace` column is legal on them. Optional; empty declares
	// nothing and is validated exactly as before. See Provenance.
	Provenance Provenance `json:"provenance,omitempty"`

	// Residual enables the residual column (unpromoted envelope remainder).
	Residual bool `json:"residual,omitempty"`
}

// ColumnDecl is one promoted column: identifier + DuckDB type name. The type
// is validated against the same anti-injection grammar the platform uses
// (letters, digits, underscore, space, parentheses, comma — e.g. VARCHAR,
// TIMESTAMP, BIGINT, DECIMAL(18,2)), not a closed enum.
type ColumnDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Role string `json:"role,omitempty"` // "" or RoleTime
}

// RoleTime marks a stream's single event-time column.
const RoleTime = "time"

// ProjectionKind enumerates the wsstore projection kinds.
type ProjectionKind string

const (
	// ProjectionCopy mirrors stream rows (optionally through TransformSQL).
	ProjectionCopy ProjectionKind = "copy"
	// ProjectionLatest keeps the newest row per key (state changes).
	ProjectionLatest ProjectionKind = "latest"
	// ProjectionDerive recomputes-and-replaces an aggregate over a copy
	// projection (aggregates that must never drift).
	ProjectionDerive ProjectionKind = "derive"
)

// ProjectionDecl is one wsstore projection over a declared stream. Scoped
// projections (the default) are bound once per workspace in the roster, with
// the platform adding the `workspace` label predicate; Unscoped projections
// are bound once, label-less, into the admin engine under the tenant's admin
// schema (cross-workspace data lives only there — the access boundary).
type ProjectionDecl struct {
	Name   string         `json:"name"`
	Stream string         `json:"stream"`
	Kind   ProjectionKind `json:"kind"`

	// KeyColumns identify a row (latest: the state key; derive: the
	// recompute-and-replace key). Required for latest and derive.
	KeyColumns []string `json:"key_columns,omitempty"`

	// TimeColumn is the recency/evict column. Required for latest.
	TimeColumn string `json:"time_column,omitempty"`

	// TransformSQL (copy only, optional) reshapes rows on the way in: a
	// {from}-tokenised BARE single SELECT — no WITH, no SELECT DISTINCT/ALL,
	// no second statement. Pure per-row; the platform re-validates and runs
	// it inside the workspace engine under the statement log and engine caps.
	TransformSQL string `json:"transform_sql,omitempty"`

	// DeriveSQL (derive only, required) is the aggregate recompute: a single
	// SELECT or WITH…SELECT reading the `{from}` token — the platform
	// substitutes it with the schema-qualified DeriveFrom table (never write
	// the table name yourself; the projector owns the schema).
	DeriveSQL string `json:"derive_sql,omitempty"`

	// DeriveFrom (derive only, required) names the copy projection in this
	// artifact the derive reads.
	DeriveFrom string `json:"derive_from,omitempty"`

	// TouchPredicate (derive only, optional) limits which upstream changes
	// trigger a recompute.
	TouchPredicate string `json:"touch_predicate,omitempty"`

	// BumpColumn / BumpTimeColumn (derive only, optional) carry the derive's
	// change-detection bump fields.
	BumpColumn     string `json:"bump_column,omitempty"`
	BumpTimeColumn string `json:"bump_time_column,omitempty"`

	// TombstoneCondition / TombstoneProjections (optional) declare tombstone
	// propagation: when a row matching the condition arrives, the named
	// projections evict the key.
	TombstoneCondition   string   `json:"tombstone_condition,omitempty"`
	TombstoneProjections []string `json:"tombstone_projections,omitempty"`

	// ScopeLabel names the label this projection binds on, instead of
	// workspace identity (S-2212). Empty — the default and every pre-0.11.0
	// artifact — means the reserved WorkspaceLabel: the platform adds
	// `workspace = '<ws>'` and the stream must carry the column. A non-empty
	// value must be declared in the lake's ScopeLabels AND listed in this
	// projection's stream Labels; the platform then resolves the values the
	// workspace claims from config at bind time and adds `<label> IN (…)`.
	//
	// Meaningless (and refused) together with Unscoped: an unscoped
	// projection binds once, label-less, into the admin engine.
	ScopeLabel string `json:"scope_label,omitempty"`

	// Unscoped binds this projection label-less into the admin engine (a
	// separate store under the tenant's admin schema, admin workspace only)
	// instead of per-workspace. This is the S-1856 seam; cross-workspace
	// data never lands in a workspace engine.
	Unscoped bool `json:"unscoped,omitempty"`
}

// ViewKind enumerates the bind-time view flavors.
type ViewKind string

const (
	// ViewKindView materializes as CREATE OR REPLACE VIEW <name> AS <sql> at
	// every bind (idempotent, follows projection reshapes).
	ViewKindView ViewKind = "view"
	// ViewKindSeed materializes as a create-only table:
	// CREATE TABLE IF NOT EXISTS <name> AS <sql> — reference data seeded
	// once, never overwritten by a re-bind.
	ViewKindSeed ViewKind = "seed"
)

// ViewDecl is one bind-time view or create-only seed table, applied through
// the platform's engine funnel at bind time (the billing/signals pattern).
// SQL is always a bare single SELECT (or WITH…SELECT) — the platform supplies
// the CREATE wrapper, so the declaration can never smuggle DDL/DML.
type ViewDecl struct {
	Name string   `json:"name"`
	Kind ViewKind `json:"kind,omitempty"` // default ViewKindView
	SQL  string   `json:"sql"`

	// Unscoped applies the view in the admin engine (over unscoped
	// projections) instead of each workspace engine.
	Unscoped bool `json:"unscoped,omitempty"`
}

// IngestDecl asks the platform to materialize the generic FILE-door ingest
// for this tenant: a runnable that walks a declared source, skips unsealed
// slices (seal margin), dedups landed files by byte hash, and lands one gen
// per file with the slice cursor as a landing constant — plus a seeded
// DISABLED job the operator enables.
type IngestDecl struct {
	// Stream is the declared stream files land into.
	Stream string `json:"stream"`

	// Source is the lake landing-source name within the tenant. Default:
	// the target Stream's name (source names must be unique per tenant).
	Source string `json:"source,omitempty"`

	// SourceKind / SourcePattern bind the platform-side source the runnable
	// walks (e.g. kind "test_local" with a directory glob). Free-form here;
	// the platform validates against its registered source kinds.
	SourceKind    string `json:"source_kind,omitempty"`
	SourcePattern string `json:"source_pattern,omitempty"`

	// SliceColumn is the declared stream column that carries the landing
	// slice cursor (the drain-surviving dedup key). Default "src_slice" when
	// empty; the stream must declare it.
	SliceColumn string `json:"slice_column,omitempty"`

	// Envelope is an inline envelope-schema YAML (the schemas/*.yaml
	// extends-pattern); EnvelopeRef references a platform-known schema by
	// name instead. At most one may be set. When neither is set, envelope
	// fields promote to declared columns by name.
	Envelope    string `json:"envelope,omitempty"`
	EnvelopeRef string `json:"envelope_ref,omitempty"`

	// SealMarginMinutes is how long after a slice-hour closes before its
	// files are considered sealed and landable. Three-valued by contract:
	// 0 (unset) = the platform default (15); >= 1 = that literal margin;
	// -1 = the seal gate DISABLED (files land immediately — dev/test_local
	// sources whose files never grow). A literal 0-minute margin is not
	// expressible; use 1.
	SealMarginMinutes int `json:"seal_margin_minutes,omitempty"`

	// Schedule is the seeded job's cron expression. Default hourly
	// ("0 * * * *") when empty. The job is always seeded DISABLED.
	Schedule string `json:"schedule,omitempty"`
}

// SourceName resolves the ingest's lake landing-source name (default: the
// target stream's name).
func (i IngestDecl) SourceName() string {
	if i.Source != "" {
		return i.Source
	}
	return i.Stream
}

// RetentionClass enumerates the retention classes.
type RetentionClass string

const (
	// RetentionWindow drops sealed files whose slice is entirely older than
	// the window, recording the drop in the gen ledger (an explicit, signed
	// absence — retention is an auditable event, not silent deletion).
	RetentionWindow RetentionClass = "window"
	// RetentionForever keeps the full mirror. It must be DECLARED — the
	// artifact has no default retention.
	RetentionForever RetentionClass = "forever"
)

// RetentionDecl is the tenant's mandatory retention declaration.
type RetentionDecl struct {
	Class RetentionClass `json:"class"`
	Days  int            `json:"days,omitempty"` // required >= 1 for "window"; must be 0 for "forever"
}

// LakeBindingSolution is v1's only Binding value: the bind roster is the
// set of workspaces bound to the announcing solution (the S-1864 rule).
const LakeBindingSolution = "solution"

// ReservedLakeNames are the in-tree lake tenants an announced tenant may
// never claim. Additive-only: growing this list is safe, shrinking it is a
// breaking change.
var ReservedLakeNames = map[string]bool{
	"conversations": true,
	"metrics":       true,
	"audit":         true,
	"solidmon":      true,
	"adf_ops":       true,
	"cdhkpi":        true,
	// The tenant name becomes the engine SCHEMA, so DuckDB's own schemas are
	// reserved too.
	"main":               true,
	"temp":               true,
	"system":             true,
	"information_schema": true,
	"pg_catalog":         true,
}

// ReservedColumns are the lake's own landing columns; declaring one is
// refused.
var ReservedColumns = map[string]bool{
	"gen":      true,
	"payload":  true,
	"residual": true,
}

// WorkspaceLabel is the reserved scoping label. A stream feeding any
// workspace-scoped projection must declare a column with this name and list
// it in Labels; the platform adds the per-workspace predicate on bind.
const WorkspaceLabel = "workspace"

// LabelClaim is one workspace's claim on one value of a scope label — the
// resolved form of the platform's per-workspace label-scope config
// ({team: [payments, billing]} becomes two claims). It is the input to
// ValidateExclusiveClaims and exists only for that: the claims themselves are
// platform config, never part of an announce.
type LabelClaim struct {
	Workspace string `json:"workspace"`
	Value     string `json:"value"`
}

// ValidateExclusiveClaims enforces the EXCLUSIVITY LAW over a resolved claim
// set: a value of an exclusive scope label (ScopeLabelDecl.Exclusive) is owned
// by exactly one workspace per lake. It is the check the platform runs at
// bind/approval — loudly, refusing the bind — and the reason it lives here is
// that both sides must refuse the same input with the same words (the
// ReservedLakeNames precedent). It reads only its arguments: no config, no
// store, no announce.
//
// It reports EVERY conflicting value, in sorted order, because the operator
// fixing a reorg wants the whole list, not the first one. A value with no
// claim is not an error here — an unclaimed value is the platform's
// unowned-count concern (nothing binds, nothing generates, and it must surface
// on the admin screen rather than vanish), and this function cannot see the
// data to know the value exists.
func ValidateExclusiveClaims(lake, label string, claims []LabelClaim) error {
	owners := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, c := range claims {
		if c.Workspace == "" || c.Value == "" {
			return fmt.Errorf("lake %q: label %q: malformed claim (workspace=%q, value=%q) — both are required", lake, label, c.Workspace, c.Value)
		}
		if seen[c.Value][c.Workspace] {
			continue // the same workspace claiming a value twice is idempotent
		}
		if seen[c.Value] == nil {
			seen[c.Value] = map[string]bool{}
		}
		seen[c.Value][c.Workspace] = true
		owners[c.Value] = append(owners[c.Value], c.Workspace)
	}

	var conflicts []string
	for value, ws := range owners {
		if len(ws) > 1 {
			sort.Strings(ws)
			conflicts = append(conflicts, fmt.Sprintf("%s=%q claimed by %s", label, value, strings.Join(ws, ", ")))
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)
	return fmt.Errorf("lake %q: label %q is exclusive — each value is owned by exactly ONE workspace, but %s. Two workspaces that can both see a row both act on it: a generative workflow opens two cases for one incident. Move the value, do not share it",
		lake, label, strings.Join(conflicts, "; "))
}

// Validate checks the declaration's structure: identifiers, reserved names,
// per-kind projection rules, SQL shape guardrails, explicit retention, and
// the binding rule. It is the partner-side fail-fast (PublishSolution calls
// it); the platform independently re-validates before materializing.
func (t LakeArtifact) Validate() error {
	if err := validLakeName(t.Name); err != nil {
		return err
	}
	if len(t.Streams) == 0 {
		return fmt.Errorf("lake %q: at least one stream required", t.Name)
	}

	streams := make(map[string]StreamDecl, len(t.Streams))
	// Scope labels are resolved before the streams so a stream's validation
	// can be checked against them, and vice versa (a declared scope label must
	// be emitted by at least one stream — otherwise it can never bind).
	scopeLabels := make(map[string]ScopeLabelDecl, len(t.ScopeLabels))
	for _, sl := range t.ScopeLabels {
		if !isIdent(sl.Name) {
			return fmt.Errorf("lake %q: scope label %q is not a valid identifier", t.Name, sl.Name)
		}
		if sl.Name == WorkspaceLabel {
			return fmt.Errorf("lake %q: %q is the reserved identity label and is never declared in scope_labels (a projection binds on it by leaving scope_label empty)", t.Name, WorkspaceLabel)
		}
		if _, dup := scopeLabels[sl.Name]; dup {
			return fmt.Errorf("lake %q: duplicate scope label %q", t.Name, sl.Name)
		}
		scopeLabels[sl.Name] = sl
	}

	// Served-name collision namespace: streams, projections and views all
	// materialize as tables/views in ONE schema (a copy projection with no
	// explicit name serves under its stream's name), so the three share one
	// namespace.
	names := map[string]bool{}
	for _, s := range t.Streams {
		if err := s.validate(t.Name); err != nil {
			return err
		}
		if _, dup := streams[s.Name]; dup {
			return fmt.Errorf("lake %q: duplicate stream %q", t.Name, s.Name)
		}
		streams[s.Name] = s
		names[s.Name] = true
	}

	// Every declared scope label must be EMITTED by at least one stream. A
	// label no stream carries can only ever bind to nothing (the fail-closed
	// rule), so declaring it is a typo, not a configuration.
	for _, sl := range t.ScopeLabels {
		emitted := false
		for _, s := range t.Streams {
			if hasLabel(s, sl.Name) {
				emitted = true
				break
			}
		}
		if !emitted {
			return fmt.Errorf("lake %q: scope label %q is not emitted by any stream (declare it as a column and list it in that stream's labels, or drop it — a label no stream carries binds to nothing)", t.Name, sl.Name)
		}
	}

	copies := map[string]ProjectionDecl{}
	projections := map[string]bool{}
	for _, p := range t.Projections {
		if p.Kind == ProjectionCopy {
			copies[p.Name] = p
		}
		projections[p.Name] = true
	}
	seenProjections := map[string]bool{}
	for _, p := range t.Projections {
		if err := p.validate(t.Name, streams, copies, projections, scopeLabels); err != nil {
			return err
		}
		if seenProjections[p.Name] {
			return fmt.Errorf("lake %q: duplicate projection %q", t.Name, p.Name)
		}
		seenProjections[p.Name] = true
		// A projection MAY serve under its own stream's name (the
		// copy-serves-as-the-stream convention); any other collision with a
		// stream or earlier projection is refused.
		if names[p.Name] && p.Name != p.Stream {
			return fmt.Errorf("lake %q: projection %q collides with another stream, projection or view", t.Name, p.Name)
		}
		names[p.Name] = true
	}

	for _, v := range t.Views {
		if err := v.validate(t.Name); err != nil {
			return err
		}
		if names[v.Name] {
			return fmt.Errorf("lake %q: view %q collides with another stream, projection or view", t.Name, v.Name)
		}
		names[v.Name] = true
	}

	// v1: the FILE door is a lake's ONLY write door, and the platform's lake
	// refuses a tenant with no sources — so at least one ingest is required.
	// (A future wire-fed lake kind would RELAX this additively.)
	if len(t.Ingests) == 0 {
		return fmt.Errorf("lake %q: at least one ingest required (v1: the FILE door is the only write door)", t.Name)
	}
	ingestSources := map[string]bool{}
	for _, ing := range t.Ingests {
		if err := ing.validate(t.Name, streams); err != nil {
			return err
		}
		src := ing.SourceName()
		if ingestSources[src] {
			return fmt.Errorf("lake %q: duplicate ingest source %q", t.Name, src)
		}
		ingestSources[src] = true
	}

	switch t.Retention.Class {
	case RetentionWindow:
		if t.Retention.Days < 1 {
			return fmt.Errorf("lake %q: retention class %q requires days >= 1", t.Name, RetentionWindow)
		}
	case RetentionForever:
		if t.Retention.Days != 0 {
			return fmt.Errorf("lake %q: retention class %q must not set days", t.Name, RetentionForever)
		}
	case "":
		return fmt.Errorf("lake %q: retention is required and always explicit (%q or %q)", t.Name, RetentionWindow, RetentionForever)
	default:
		return fmt.Errorf("lake %q: unknown retention class %q", t.Name, t.Retention.Class)
	}

	if t.Binding != LakeBindingSolution {
		return fmt.Errorf("lake %q: binding must be %q (v1's only roster rule)", t.Name, LakeBindingSolution)
	}
	return nil
}

func (s StreamDecl) validate(tenant string) error {
	if !isIdent(s.Name) {
		return fmt.Errorf("lake %q: stream name %q is not a valid identifier", tenant, s.Name)
	}
	if len(s.Columns) == 0 {
		return fmt.Errorf("lake %q: stream %q declares no columns", tenant, s.Name)
	}
	cols := map[string]bool{}
	timeCols := 0
	for _, c := range s.Columns {
		if !isIdent(c.Name) {
			return fmt.Errorf("lake %q: stream %q: column name %q is not a valid identifier", tenant, s.Name, c.Name)
		}
		if ReservedColumns[strings.ToLower(c.Name)] {
			return fmt.Errorf("lake %q: stream %q: column %q is reserved by the lake", tenant, s.Name, c.Name)
		}
		if cols[c.Name] {
			return fmt.Errorf("lake %q: stream %q: duplicate column %q", tenant, s.Name, c.Name)
		}
		cols[c.Name] = true
		if !isDuckType(c.Type) {
			return fmt.Errorf("lake %q: stream %q: column %q has invalid type %q", tenant, s.Name, c.Name, c.Type)
		}
		switch c.Role {
		case "":
		case RoleTime:
			timeCols++
		default:
			return fmt.Errorf("lake %q: stream %q: column %q has unknown role %q", tenant, s.Name, c.Name, c.Role)
		}
	}
	if timeCols != 1 {
		return fmt.Errorf("lake %q: stream %q must declare exactly one role=%q column (got %d)", tenant, s.Name, RoleTime, timeCols)
	}
	for _, l := range s.Labels {
		if !cols[l] {
			return fmt.Errorf("lake %q: stream %q: label %q is not a declared column", tenant, s.Name, l)
		}
	}

	// The provenance rule (S-2212): where `workspace` may appear in the lake.
	switch s.Provenance {
	case "":
		// Undeclared — validated exactly as before this field existed.
	case ProvenanceSource, ProvenanceProduct:
		if cols[WorkspaceLabel] {
			return fmt.Errorf("lake %q: stream %q is %s data and must not declare a %q column — source and product rows carry their domain labels; the org mapping lives in config and resolves at bind (a workspace stamp here freezes today's org chart into an immutable record and makes a reorg a migration)",
				tenant, s.Name, s.Provenance, WorkspaceLabel)
		}
	case ProvenanceExhaust:
		if !cols[WorkspaceLabel] {
			return fmt.Errorf("lake %q: stream %q is execution exhaust and must declare a %q column — the workspace a run executed under is a fact about the row, stamped at open and true forever after",
				tenant, s.Name, WorkspaceLabel)
		}
	default:
		return fmt.Errorf("lake %q: stream %q: unknown provenance %q (known: %q, %q, %q)",
			tenant, s.Name, s.Provenance, ProvenanceSource, ProvenanceProduct, ProvenanceExhaust)
	}
	return nil
}

func (p ProjectionDecl) validate(tenant string, streams map[string]StreamDecl, copies map[string]ProjectionDecl, projections map[string]bool, scopeLabels map[string]ScopeLabelDecl) error {
	if !isIdent(p.Name) {
		return fmt.Errorf("lake %q: projection name %q is not a valid identifier", tenant, p.Name)
	}
	stream, ok := streams[p.Stream]
	if !ok {
		return fmt.Errorf("lake %q: projection %q references undeclared stream %q", tenant, p.Name, p.Stream)
	}

	if p.Unscoped {
		if p.ScopeLabel != "" {
			return fmt.Errorf("lake %q: projection %q sets scope_label %q and unscoped together — an unscoped projection binds ONCE, label-less, into the admin engine",
				tenant, p.Name, p.ScopeLabel)
		}
	} else {
		// The bind label: the reserved workspace identity by default, or the
		// declared scope label. Either way the stream must EMIT it — a
		// projection scoped on a label its rows do not carry fails closed
		// (binds to nothing), so it is refused here instead.
		label := p.ScopeLabel
		if label == "" {
			label = WorkspaceLabel
		} else if _, declared := scopeLabels[label]; !declared {
			return fmt.Errorf("lake %q: projection %q binds on scope label %q, which the lake does not declare in scope_labels",
				tenant, p.Name, label)
		}
		if !hasLabel(stream, label) {
			if label == WorkspaceLabel {
				return fmt.Errorf("lake %q: projection %q is workspace-scoped but stream %q does not declare the %q label (source/product streams are never workspace-stamped — declare a scope label and bind on that instead)",
					tenant, p.Name, p.Stream, WorkspaceLabel)
			}
			return fmt.Errorf("lake %q: projection %q binds on scope label %q but stream %q does not emit it (list %q in the stream's labels)",
				tenant, p.Name, label, p.Stream, label)
		}
	}

	// Column references can only be checked against the stream when no
	// transform reshapes the rows; otherwise they name transform output and
	// are checked as identifiers only.
	checkCol := func(field, col string) error {
		if !isIdent(col) {
			return fmt.Errorf("lake %q: projection %q: %s %q is not a valid identifier", tenant, p.Name, field, col)
		}
		if p.TransformSQL == "" && p.Kind != ProjectionDerive && !streamHasColumn(stream, col) {
			return fmt.Errorf("lake %q: projection %q: %s %q is not a column of stream %q", tenant, p.Name, field, col, p.Stream)
		}
		return nil
	}

	// Derive-only fields are fenced per kind: Bump*/TouchPredicate/Tombstone*
	// have no meaning on copy/latest, and a silently-ignored field is a wire
	// trap (the author believes tombstones work).
	deriveOnlySet := p.TouchPredicate != "" || p.BumpColumn != "" || p.BumpTimeColumn != "" ||
		p.TombstoneCondition != "" || len(p.TombstoneProjections) > 0

	switch p.Kind {
	case ProjectionCopy:
		if p.DeriveSQL != "" || p.DeriveFrom != "" || deriveOnlySet {
			return fmt.Errorf("lake %q: projection %q: derive-only fields on a copy projection", tenant, p.Name)
		}
		if p.TransformSQL != "" {
			if err := validateBareSelect(p.TransformSQL, false); err != nil {
				return fmt.Errorf("lake %q: projection %q: transform_sql: %w", tenant, p.Name, err)
			}
			if !strings.Contains(p.TransformSQL, "{from}") {
				return fmt.Errorf("lake %q: projection %q: transform_sql must read the {from} token (the platform substitutes the schema-qualified stream source)", tenant, p.Name)
			}
		}
	case ProjectionLatest:
		if p.TransformSQL != "" || p.DeriveSQL != "" || p.DeriveFrom != "" || deriveOnlySet {
			return fmt.Errorf("lake %q: projection %q: transform/derive-only fields on a latest projection", tenant, p.Name)
		}
		if len(p.KeyColumns) == 0 {
			return fmt.Errorf("lake %q: projection %q: latest requires key_columns", tenant, p.Name)
		}
		if p.TimeColumn == "" {
			return fmt.Errorf("lake %q: projection %q: latest requires time_column", tenant, p.Name)
		}
	case ProjectionDerive:
		if p.TransformSQL != "" {
			return fmt.Errorf("lake %q: projection %q: transform_sql on a derive projection", tenant, p.Name)
		}
		if p.DeriveSQL == "" || p.DeriveFrom == "" {
			return fmt.Errorf("lake %q: projection %q: derive requires derive_sql and derive_from", tenant, p.Name)
		}
		from, isCopy := copies[p.DeriveFrom]
		if !isCopy {
			return fmt.Errorf("lake %q: projection %q: derive_from %q is not a copy projection in this artifact", tenant, p.Name, p.DeriveFrom)
		}
		if len(p.KeyColumns) == 0 {
			return fmt.Errorf("lake %q: projection %q: derive requires key_columns", tenant, p.Name)
		}
		// The platform's touched-key scoping reads a derive's key columns FROM
		// THE SOURCE TABLE (which keys did this gen touch?), so every key must
		// be a column of the DeriveFrom copy — a computed (SELECT-expression)
		// key cannot exist. Checkable only when the copy is transform-less
		// (its surface = its stream); a transform reshapes the surface, so the
		// keys are ident-checked only. The M4 proof run found this the hard
		// way: a derive keyed on date_trunc('week', …) fails at Bind.
		if from.TransformSQL == "" {
			if srcStream, ok := streams[from.Stream]; ok {
				for _, k := range p.KeyColumns {
					if !streamHasColumn(srcStream, k) {
						return fmt.Errorf("lake %q: projection %q: derive key column %q is not a column of derive_from %q (stream %q) — touched-key scoping reads keys from the source table; carry the key on the wire", tenant, p.Name, k, p.DeriveFrom, from.Stream)
					}
				}
			}
		}
		if !strings.Contains(p.DeriveSQL, "{from}") {
			return fmt.Errorf("lake %q: projection %q: derive_sql must read the {from} token (the platform substitutes the schema-qualified derive_from table)", tenant, p.Name)
		}
		if (p.BumpColumn == "") != (p.BumpTimeColumn == "") {
			return fmt.Errorf("lake %q: projection %q: bump_column and bump_time_column must be set together", tenant, p.Name)
		}
		if len(p.TombstoneProjections) > 0 && p.TombstoneCondition == "" {
			return fmt.Errorf("lake %q: projection %q: tombstone_projections set without tombstone_condition", tenant, p.Name)
		}
		for _, tp := range p.TombstoneProjections {
			if !projections[tp] {
				return fmt.Errorf("lake %q: projection %q: tombstone projection %q is not declared in this artifact", tenant, p.Name, tp)
			}
		}
		if err := validateBareSelect(p.DeriveSQL, true); err != nil {
			return fmt.Errorf("lake %q: projection %q: derive_sql: %w", tenant, p.Name, err)
		}
	default:
		return fmt.Errorf("lake %q: projection %q: unknown kind %q", tenant, p.Name, p.Kind)
	}

	for _, k := range p.KeyColumns {
		if err := checkCol("key column", k); err != nil {
			return err
		}
	}
	if p.TimeColumn != "" {
		if err := checkCol("time column", p.TimeColumn); err != nil {
			return err
		}
	}
	for _, opt := range []struct{ field, val string }{
		{"bump_column", p.BumpColumn}, {"bump_time_column", p.BumpTimeColumn},
	} {
		if opt.val != "" && !isIdent(opt.val) {
			return fmt.Errorf("lake %q: projection %q: %s %q is not a valid identifier", tenant, p.Name, opt.field, opt.val)
		}
	}
	return nil
}

func (v ViewDecl) validate(tenant string) error {
	if !isIdent(v.Name) {
		return fmt.Errorf("lake %q: view name %q is not a valid identifier", tenant, v.Name)
	}
	switch v.Kind {
	case "", ViewKindView, ViewKindSeed:
	default:
		return fmt.Errorf("lake %q: view %q: unknown kind %q", tenant, v.Name, v.Kind)
	}
	if err := validateBareSelect(v.SQL, true); err != nil {
		return fmt.Errorf("lake %q: view %q: sql: %w", tenant, v.Name, err)
	}
	return nil
}

func (i IngestDecl) validate(tenant string, streams map[string]StreamDecl) error {
	stream, ok := streams[i.Stream]
	if !ok {
		return fmt.Errorf("lake %q: ingest references undeclared stream %q", tenant, i.Stream)
	}
	if i.Source != "" && !isIdent(i.Source) {
		return fmt.Errorf("lake %q: ingest source %q is not a valid identifier", tenant, i.Source)
	}

	slice := i.SliceColumn
	if slice == "" {
		slice = "src_slice"
	}
	if !streamHasColumn(stream, slice) {
		return fmt.Errorf("lake %q: ingest slice column %q is not a column of stream %q", tenant, slice, i.Stream)
	}
	if i.Envelope != "" && i.EnvelopeRef != "" {
		return fmt.Errorf("lake %q: ingest declares both envelope and envelope_ref", tenant)
	}
	if i.SealMarginMinutes < -1 {
		return fmt.Errorf("lake %q: ingest seal_margin_minutes must be -1 (gate disabled), 0 (default) or >= 1", tenant)
	}
	return nil
}

func validLakeName(name string) error {
	if name == "" {
		return fmt.Errorf("lake declaration has no name")
	}
	if !isLowerIdent(name) {
		return fmt.Errorf("lake name %q must be a lowercase identifier ([a-z][a-z0-9_]*)", name)
	}
	if ReservedLakeNames[name] || strings.HasPrefix(name, "solid") {
		return fmt.Errorf("lake name %q is reserved", name)
	}
	if strings.HasSuffix(name, "_admin") {
		return fmt.Errorf("lake name %q is reserved (the _admin suffix is the admin-schema namespace: lake foo's unscoped surfaces serve as foo_admin)", name)
	}
	return nil
}

func hasLabel(s StreamDecl, label string) bool {
	for _, l := range s.Labels {
		if l == label {
			return true
		}
	}
	return false
}

func streamHasColumn(s StreamDecl, name string) bool {
	for _, c := range s.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

// isIdent mirrors the lake's identifier grammar: a letter followed by
// letters, digits or underscores. A leading underscore (and so the lake's
// internal `__` prefix) is refused by construction.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

func isLowerIdent(s string) bool {
	return isIdent(s) && strings.ToLower(s) == s
}

// isDuckType mirrors the platform's anti-injection type grammar: letters,
// digits, underscore, space, parentheses and comma — a grammar, not a closed
// enum, so DECIMAL(18,2) and friends pass and injection vectors do not.
func isDuckType(t string) bool {
	if strings.TrimSpace(t) == "" {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == ' ' || r == '(' || r == ')' || r == ',':
		default:
			return false
		}
	}
	return true
}

// validateBareSelect enforces the single-statement bare-SELECT guardrail the
// platform's projector applies: one statement, starting SELECT (or, when
// allowWith, WITH), never SELECT DISTINCT/ALL (the engine splices bookkeeping
// columns after the leading SELECT). An author guardrail mirrored from the
// platform, not a security boundary — the platform re-validates and the SQL
// runs under the statement log and engine caps regardless. KNOWN LIMITATION
// (matches the platform's own scan): a literal ';' inside a string constant
// false-positives as a second statement — rewrite the literal (e.g. CHR(59))
// or restructure; accepted because the platform-side check trips on the same
// input, so failing early here is strictly kinder.
func validateBareSelect(q string, allowWith bool) error {
	s := strings.TrimSpace(q)
	if s == "" {
		return fmt.Errorf("empty SQL")
	}
	if i := strings.IndexByte(s, ';'); i >= 0 && strings.TrimSpace(s[i+1:]) != "" {
		return fmt.Errorf("multiple statements")
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), ";")
	upper := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		rest := strings.TrimSpace(upper[len("SELECT"):])
		if !allowWith && (strings.HasPrefix(rest, "DISTINCT") || strings.HasPrefix(rest, "ALL ")) {
			return fmt.Errorf("SELECT DISTINCT/ALL is not supported here")
		}
	case strings.HasPrefix(upper, "WITH"):
		if !allowWith {
			return fmt.Errorf("WITH…SELECT is not supported here (bare SELECT only)")
		}
	default:
		return fmt.Errorf("must be a SELECT")
	}
	return nil
}
