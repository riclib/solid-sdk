package contract

import (
	"fmt"
	"strings"
)

// TenantArtifact is the leaf payload for an ArtifactTenant — a lake-tenant
// declaration the solution ships (S-1874, "demos over the lake"). It is the
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
type TenantArtifact struct {
	// Name is BOTH the artifact leaf id (`<solution>.tenant.<name>`) and the
	// lake tenant identifier. Validated: lowercase identifier
	// ([a-z][a-z0-9_]*), refused when reserved (the in-tree tenants —
	// "conversations", "metrics", "audit", solidmon's "adf_ops"/"cdhkpi" —
	// and anything prefixed "solid").
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

	// Ingest, when set, materializes the generic FILE-door ingest runnable
	// for this tenant plus a seeded DISABLED job (the operator enables it —
	// the S-1852 convention).
	Ingest *IngestDecl `json:"ingest,omitempty"`

	// Retention is REQUIRED and always explicit: "forever" must be declared,
	// never defaulted (the full-mirror ruling is per-system). Demos declare
	// {Class: "window", Days: 90}.
	Retention RetentionDecl `json:"retention"`

	// Binding names the roster rule: which workspaces the platform binds the
	// tenant's projections into. v1's only value is TenantBindingSolution
	// ("solution"): the roster is the workspaces bound to the announcing
	// solution (the S-1864 rule).
	Binding string `json:"binding"`
}

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
	// feeding any workspace-scoped (non-Unscoped) projection must declare a
	// `workspace` column and list it here.
	Labels []string `json:"labels,omitempty"`

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

	// Source is the lake landing-source name within the tenant. Default
	// "ingest" when empty.
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

	// SealMarginMinutes is how long after a slice closes before its files
	// are considered sealed and landable. Default 15 when 0.
	SealMarginMinutes int `json:"seal_margin_minutes,omitempty"`

	// Schedule is the seeded job's cron expression. Default hourly
	// ("0 * * * *") when empty. The job is always seeded DISABLED.
	Schedule string `json:"schedule,omitempty"`
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

// TenantBindingSolution is v1's only Binding value: the bind roster is the
// set of workspaces bound to the announcing solution (the S-1864 rule).
const TenantBindingSolution = "solution"

// reservedTenantNames are the in-tree lake tenants an announced tenant may
// never claim. Additive-only: growing this list is safe, shrinking it is a
// breaking change.
var reservedTenantNames = map[string]bool{
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

// reservedColumns are the lake's own landing columns; declaring one is
// refused.
var reservedColumns = map[string]bool{
	"gen":      true,
	"payload":  true,
	"residual": true,
}

// WorkspaceLabel is the reserved scoping label. A stream feeding any
// workspace-scoped projection must declare a column with this name and list
// it in Labels; the platform adds the per-workspace predicate on bind.
const WorkspaceLabel = "workspace"

// Validate checks the declaration's structure: identifiers, reserved names,
// per-kind projection rules, SQL shape guardrails, explicit retention, and
// the binding rule. It is the partner-side fail-fast (PublishSolution calls
// it); the platform independently re-validates before materializing.
func (t TenantArtifact) Validate() error {
	if err := validTenantName(t.Name); err != nil {
		return err
	}
	if len(t.Streams) == 0 {
		return fmt.Errorf("tenant %q: at least one stream required", t.Name)
	}

	streams := make(map[string]StreamDecl, len(t.Streams))
	for _, s := range t.Streams {
		if err := s.validate(t.Name); err != nil {
			return err
		}
		if _, dup := streams[s.Name]; dup {
			return fmt.Errorf("tenant %q: duplicate stream %q", t.Name, s.Name)
		}
		streams[s.Name] = s
	}

	copies := map[string]bool{}
	names := map[string]bool{}
	for _, p := range t.Projections {
		if p.Kind == ProjectionCopy {
			copies[p.Name] = true
		}
	}
	for _, p := range t.Projections {
		if err := p.validate(t.Name, streams, copies); err != nil {
			return err
		}
		if names[p.Name] {
			return fmt.Errorf("tenant %q: duplicate projection %q", t.Name, p.Name)
		}
		names[p.Name] = true
	}

	for _, v := range t.Views {
		if err := v.validate(t.Name); err != nil {
			return err
		}
		if names[v.Name] {
			return fmt.Errorf("tenant %q: view %q collides with another projection or view", t.Name, v.Name)
		}
		names[v.Name] = true
	}

	if t.Ingest != nil {
		if err := t.Ingest.validate(t.Name, streams); err != nil {
			return err
		}
	}

	switch t.Retention.Class {
	case RetentionWindow:
		if t.Retention.Days < 1 {
			return fmt.Errorf("tenant %q: retention class %q requires days >= 1", t.Name, RetentionWindow)
		}
	case RetentionForever:
		if t.Retention.Days != 0 {
			return fmt.Errorf("tenant %q: retention class %q must not set days", t.Name, RetentionForever)
		}
	case "":
		return fmt.Errorf("tenant %q: retention is required and always explicit (%q or %q)", t.Name, RetentionWindow, RetentionForever)
	default:
		return fmt.Errorf("tenant %q: unknown retention class %q", t.Name, t.Retention.Class)
	}

	if t.Binding != TenantBindingSolution {
		return fmt.Errorf("tenant %q: binding must be %q (v1's only roster rule)", t.Name, TenantBindingSolution)
	}
	return nil
}

func (s StreamDecl) validate(tenant string) error {
	if !isIdent(s.Name) {
		return fmt.Errorf("tenant %q: stream name %q is not a valid identifier", tenant, s.Name)
	}
	if len(s.Columns) == 0 {
		return fmt.Errorf("tenant %q: stream %q declares no columns", tenant, s.Name)
	}
	cols := map[string]bool{}
	timeCols := 0
	for _, c := range s.Columns {
		if !isIdent(c.Name) {
			return fmt.Errorf("tenant %q: stream %q: column name %q is not a valid identifier", tenant, s.Name, c.Name)
		}
		if reservedColumns[strings.ToLower(c.Name)] {
			return fmt.Errorf("tenant %q: stream %q: column %q is reserved by the lake", tenant, s.Name, c.Name)
		}
		if cols[c.Name] {
			return fmt.Errorf("tenant %q: stream %q: duplicate column %q", tenant, s.Name, c.Name)
		}
		cols[c.Name] = true
		if !isDuckType(c.Type) {
			return fmt.Errorf("tenant %q: stream %q: column %q has invalid type %q", tenant, s.Name, c.Name, c.Type)
		}
		switch c.Role {
		case "":
		case RoleTime:
			timeCols++
		default:
			return fmt.Errorf("tenant %q: stream %q: column %q has unknown role %q", tenant, s.Name, c.Name, c.Role)
		}
	}
	if timeCols != 1 {
		return fmt.Errorf("tenant %q: stream %q must declare exactly one role=%q column (got %d)", tenant, s.Name, RoleTime, timeCols)
	}
	for _, l := range s.Labels {
		if !cols[l] {
			return fmt.Errorf("tenant %q: stream %q: label %q is not a declared column", tenant, s.Name, l)
		}
	}
	return nil
}

func (p ProjectionDecl) validate(tenant string, streams map[string]StreamDecl, copies map[string]bool) error {
	if !isIdent(p.Name) {
		return fmt.Errorf("tenant %q: projection name %q is not a valid identifier", tenant, p.Name)
	}
	stream, ok := streams[p.Stream]
	if !ok {
		return fmt.Errorf("tenant %q: projection %q references undeclared stream %q", tenant, p.Name, p.Stream)
	}

	if !p.Unscoped {
		if !hasLabel(stream, WorkspaceLabel) {
			return fmt.Errorf("tenant %q: projection %q is workspace-scoped but stream %q does not declare the %q label",
				tenant, p.Name, p.Stream, WorkspaceLabel)
		}
	}

	// Column references can only be checked against the stream when no
	// transform reshapes the rows; otherwise they name transform output and
	// are checked as identifiers only.
	checkCol := func(field, col string) error {
		if !isIdent(col) {
			return fmt.Errorf("tenant %q: projection %q: %s %q is not a valid identifier", tenant, p.Name, field, col)
		}
		if p.TransformSQL == "" && p.Kind != ProjectionDerive && !streamHasColumn(stream, col) {
			return fmt.Errorf("tenant %q: projection %q: %s %q is not a column of stream %q", tenant, p.Name, field, col, p.Stream)
		}
		return nil
	}

	switch p.Kind {
	case ProjectionCopy:
		if p.DeriveSQL != "" || p.DeriveFrom != "" || p.TouchPredicate != "" {
			return fmt.Errorf("tenant %q: projection %q: derive fields on a copy projection", tenant, p.Name)
		}
		if p.TransformSQL != "" {
			if err := validateBareSelect(p.TransformSQL, false); err != nil {
				return fmt.Errorf("tenant %q: projection %q: transform_sql: %w", tenant, p.Name, err)
			}
		}
	case ProjectionLatest:
		if p.TransformSQL != "" || p.DeriveSQL != "" || p.DeriveFrom != "" {
			return fmt.Errorf("tenant %q: projection %q: transform/derive fields on a latest projection", tenant, p.Name)
		}
		if len(p.KeyColumns) == 0 {
			return fmt.Errorf("tenant %q: projection %q: latest requires key_columns", tenant, p.Name)
		}
		if p.TimeColumn == "" {
			return fmt.Errorf("tenant %q: projection %q: latest requires time_column", tenant, p.Name)
		}
	case ProjectionDerive:
		if p.TransformSQL != "" {
			return fmt.Errorf("tenant %q: projection %q: transform_sql on a derive projection", tenant, p.Name)
		}
		if p.DeriveSQL == "" || p.DeriveFrom == "" {
			return fmt.Errorf("tenant %q: projection %q: derive requires derive_sql and derive_from", tenant, p.Name)
		}
		if !copies[p.DeriveFrom] {
			return fmt.Errorf("tenant %q: projection %q: derive_from %q is not a copy projection in this artifact", tenant, p.Name, p.DeriveFrom)
		}
		if len(p.KeyColumns) == 0 {
			return fmt.Errorf("tenant %q: projection %q: derive requires key_columns", tenant, p.Name)
		}
		if !strings.Contains(p.DeriveSQL, "{from}") {
			return fmt.Errorf("tenant %q: projection %q: derive_sql must read the {from} token (the platform substitutes the schema-qualified derive_from table)", tenant, p.Name)
		}
		if (p.BumpColumn == "") != (p.BumpTimeColumn == "") {
			return fmt.Errorf("tenant %q: projection %q: bump_column and bump_time_column must be set together", tenant, p.Name)
		}
		if err := validateBareSelect(p.DeriveSQL, true); err != nil {
			return fmt.Errorf("tenant %q: projection %q: derive_sql: %w", tenant, p.Name, err)
		}
	default:
		return fmt.Errorf("tenant %q: projection %q: unknown kind %q", tenant, p.Name, p.Kind)
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
			return fmt.Errorf("tenant %q: projection %q: %s %q is not a valid identifier", tenant, p.Name, opt.field, opt.val)
		}
	}
	return nil
}

func (v ViewDecl) validate(tenant string) error {
	if !isIdent(v.Name) {
		return fmt.Errorf("tenant %q: view name %q is not a valid identifier", tenant, v.Name)
	}
	switch v.Kind {
	case "", ViewKindView, ViewKindSeed:
	default:
		return fmt.Errorf("tenant %q: view %q: unknown kind %q", tenant, v.Name, v.Kind)
	}
	if err := validateBareSelect(v.SQL, true); err != nil {
		return fmt.Errorf("tenant %q: view %q: sql: %w", tenant, v.Name, err)
	}
	return nil
}

func (i IngestDecl) validate(tenant string, streams map[string]StreamDecl) error {
	stream, ok := streams[i.Stream]
	if !ok {
		return fmt.Errorf("tenant %q: ingest references undeclared stream %q", tenant, i.Stream)
	}
	if i.Source != "" && !isIdent(i.Source) {
		return fmt.Errorf("tenant %q: ingest source %q is not a valid identifier", tenant, i.Source)
	}
	slice := i.SliceColumn
	if slice == "" {
		slice = "src_slice"
	}
	if !streamHasColumn(stream, slice) {
		return fmt.Errorf("tenant %q: ingest slice column %q is not a column of stream %q", tenant, slice, i.Stream)
	}
	if i.Envelope != "" && i.EnvelopeRef != "" {
		return fmt.Errorf("tenant %q: ingest declares both envelope and envelope_ref", tenant)
	}
	if i.SealMarginMinutes < 0 {
		return fmt.Errorf("tenant %q: ingest seal_margin_minutes must be >= 0", tenant)
	}
	return nil
}

func validTenantName(name string) error {
	if name == "" {
		return fmt.Errorf("tenant declaration has no name")
	}
	if !isLowerIdent(name) {
		return fmt.Errorf("tenant name %q must be a lowercase identifier ([a-z][a-z0-9_]*)", name)
	}
	if reservedTenantNames[name] || strings.HasPrefix(name, "solid") {
		return fmt.Errorf("tenant name %q is reserved", name)
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
// runs under the statement log and engine caps regardless.
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
