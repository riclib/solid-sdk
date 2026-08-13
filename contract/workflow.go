package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// WorkflowKind names the GRAMMAR a WorkflowArtifact's Body is written in. It
// is a routing discriminator, not a feature flag: the platform hands the body
// to the parser this field names and NEVER sniffs the YAML to guess. Two
// grammars ship today, each with its own contract document:
//
//   - WorkflowKindSkill — docs/workflow-defs.md (SHIPPED 1.0.1): the pure-skill
//     definition. A triggered, anchored conversation that runs ONE named skill
//     over a resolved period.
//   - WorkflowKindMechanic — docs/mechanic-defs.md: the mechanic def. A stepped
//     definition the platform's mechanic engine runs per case: retrieval, LLM
//     steps, declared config, gates, close conditions.
//
// An EMPTY Kind means WorkflowKindSkill. Every producer published before this
// field existed announced a pure-skill def, so the zero value must keep meaning
// exactly what it meant then (the additive-only rule).
type WorkflowKind string

const (
	// WorkflowKindSkill is the v1 pure-skill definition grammar
	// (docs/workflow-defs.md). Also the meaning of an empty Kind.
	WorkflowKindSkill WorkflowKind = "skill"
	// WorkflowKindMechanic is the mechanic def grammar (docs/mechanic-defs.md).
	WorkflowKindMechanic WorkflowKind = "mechanic"
)

// WorkflowArtifact is the leaf payload for an ArtifactWorkflow — a workflow
// definition the solution ships. Like a skill it is PURE CONTROL-PLANE
// CONTENT: a declarative definition the platform parses and runs, with no data
// access of its own.
//
// Body is the definition YAML VERBATIM — opaque bytes this SDK carries and
// never interprets. Both grammars (Kind) ride the same field; a pure-skill def
// is a handful of lines and a mechanic def is ~100 B per step, so either sits
// comfortably under MaxArtifactSize in one KV leaf.
//
// # Why the body is never re-marshalled
//
// The CONTENT HASH of Body is the def's version identity (BodyHash): the
// platform stores a def under its hash, and re-announcing the same bytes is a
// no-op while one changed character is a new version. That identity only holds
// if the bytes survive the wire unchanged — so nothing in this SDK parses the
// YAML and re-emits it. A round trip through the announce path (marshal → KV →
// unmarshal) returns byte-identical Body, including comments, key order,
// indentation style, blank lines and the presence or absence of a trailing
// newline. Re-serializing YAML would silently rewrite all five and mint a new
// version identity for an unchanged def.
type WorkflowArtifact struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source,omitempty"` // the solution that ships it
	Tags        []string `json:"tags,omitempty"`

	// Kind names the grammar Body is written in. Empty = WorkflowKindSkill
	// (the pre-0.11.0 meaning, frozen).
	Kind WorkflowKind `json:"kind,omitempty"`

	// Body is the definition YAML, verbatim and opaque. See the type doc: it
	// is never parsed or re-emitted by this SDK, because its content hash is
	// the def's version identity.
	Body string `json:"body"`
}

// EffectiveKind resolves the grammar this artifact declares, mapping the empty
// Kind to WorkflowKindSkill. Read the kind through this, never through the
// field, so the frozen zero-value meaning is applied in exactly one place.
func (w WorkflowArtifact) EffectiveKind() WorkflowKind {
	if w.Kind == "" {
		return WorkflowKindSkill
	}
	return w.Kind
}

// DefVersion is the def's VERSION IDENTITY: the first 12 hex characters of the
// SHA-256 of Body's bytes. Same bytes → same version; one changed character —
// a comment, a space — → a new version. There is NO normalization: the digest
// covers the bytes exactly as announced.
//
// The 12-character prefix is not a stylistic choice: it is byte-for-byte what
// the platform computes and stamps as `def_version` on every case and every
// invocation record. This function lives in contract/ for the same reason
// ReservedLakeNames does — both sides must derive the same identity from the
// same leaf, and two implementations of "hash the body" eventually disagree
// about encoding or truncation. It is a pure function of one field (stdlib
// only), not behavior the wire carries.
//
// A def has no `version:` key, by design: an authored version number is a
// promise someone forgets to keep, while the content hash cannot be wrong. It
// is also why operational knobs live OUTSIDE the def (see the config document
// in docs/mechanic-defs.md) — turning a dial must not re-version the def.
func (w WorkflowArtifact) DefVersion() string {
	sum := sha256.Sum256([]byte(w.Body))
	return hex.EncodeToString(sum[:])[:12]
}

// Validate is the partner-side fail-fast PublishSolution applies: structural
// only, deliberately shallow. It checks the identity and the grammar
// discriminator — never the Body, which is opaque here BY CONTRACT (the
// grammar's own validator is platform-side, and a def that this SDK could
// fully validate would be a def this SDK had to parse).
//
// The platform re-validates independently before materializing; this is a
// courtesy, not the gate.
func (w WorkflowArtifact) Validate() error {
	if w.ID == "" {
		return fmt.Errorf("workflow artifact has no id")
	}
	switch w.EffectiveKind() {
	case WorkflowKindSkill, WorkflowKindMechanic:
	default:
		return fmt.Errorf("workflow %q: unknown kind %q (known: %q, %q; empty means %q)",
			w.ID, w.Kind, WorkflowKindSkill, WorkflowKindMechanic, WorkflowKindSkill)
	}
	if w.Body == "" {
		return fmt.Errorf("workflow %q: body is empty (the def YAML travels verbatim in body)", w.ID)
	}
	return nil
}
