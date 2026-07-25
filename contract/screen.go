package contract

// screen.go is the render-over-NATS envelope: the wire a solution answers so it
// can own its own configuration SCREENS without the platform compiling its UI
// in. The platform asks; the solution renders and validates; **the platform
// persists**. A solution never writes configuration — that rule is what makes a
// partner-shipped screen safe to mount inside the operator's own shell.
//
// Origin: the platform's S-1886 spike (`infra/screenwire`), lifted here on
// S-1889 once the contract settled. The two platform-only halves of that package
// did NOT come along:
//
//   - the CLIENT (`Render` / `Submit`) — the platform is the only caller; a
//     solution asking another solution to render is not a thing.
//   - the SANITIZER — the allowlist gate is the platform's boundary defence
//     against the fragment it just received, and a solution running it on its
//     own output proves nothing. `docs/screens.md` documents the allowlist a
//     fragment must survive so you can write inside it; the gate itself stays
//     platform-side.
//
// # Shape
//
// Two verbs, one subject family (see ScreenSubject):
//
//	ui.<solution>.<point>.<screen>   request-reply, core NATS (not JetStream)
//
// Core NATS on purpose: a render sits in front of a browser request that is
// already waiting. A screen the solution failed to answer must fall back NOW
// (the platform renders the generic view of the same object), not replay later —
// configuration is never hostage to the solution process. Nothing here queues,
// retries, or persists.

// ScreenVerb is the envelope's operation. Exactly two, and the pair is closed by
// design: a screen either shows itself or takes a submission. Anything richer
// (streaming fragments, arbitrary pages, solution-shipped assets) is out of the
// v1 cut and would need a different transport, not another verb.
type ScreenVerb string

const (
	// ScreenVerbRender asks the solution for the screen's HTML fragment.
	ScreenVerbRender ScreenVerb = "render"
	// ScreenVerbSubmit hands the solution a form submission. The solution
	// validates and normalizes; the PLATFORM persists. See ScreenReply.Object.
	ScreenVerbSubmit ScreenVerb = "submit"
)

// ScreenPointWorkspaceSettings is the one screen POINT open in v1: a tab on the
// workspace-admin cockpit, editing a workspace-grained configuration object.
//
// A point is WHERE in the platform's UI a screen mounts, and it carries a
// contract of its own — what the platform guarantees about the surrounding
// chrome and what it requires of the declaration. For this point:
// `ObjectKind` is required, the object is workspace-grained (one object per
// workspace, `ObjectID` = the workspace slug), and the fragment renders inside
// the platform's own `<form>` on a cockpit tab. See `docs/screens.md` for the
// full point registry; every other point is RESERVED, not yet open.
const ScreenPointWorkspaceSettings = "workspace.settings"

// screenPoints is the registry of points this SDK version knows. Additive-only:
// a point is added when the platform opens it, never repurposed. A solution
// built against a NEWER SDK may declare a point an older platform has never
// heard of — that platform drops the declaration (the tab simply does not
// appear), which is why the registry is a soft check here rather than a publish
// error. See ScreenPointKnown.
var screenPoints = map[string]bool{
	ScreenPointWorkspaceSettings: true,
}

// ScreenPointKnown reports whether point is in this SDK version's point
// registry. The PLATFORM makes the real decision — it drops any declaration
// whose point it does not serve — so treat this as a pre-flight check, not a
// guarantee.
func ScreenPointKnown(point string) bool { return screenPoints[point] }

// ScreenPoints returns the points this SDK version knows, for a solution that
// wants to render its own diagnostics.
func ScreenPoints() []string { return []string{ScreenPointWorkspaceSettings} }

// ScreenDescriptor is one declared configuration screen — the manifest-header
// declaration that sits beside FireDescriptor (a CAPABILITY declaration, not an
// artifact body, so it rides in the small manifest index rather than a KV leaf).
// It is pure metadata: where the screen mounts, what it is called, what to label
// its tab, and which configuration object kind it edits.
//
// Declaring the screen is what earns the subject grant: at approve time the
// platform grants the partner account `ui.<solution>.>`, so a solution can only
// ever serve screens it announced. No declaration, no tab, no subject
// permission.
//
// ObjectKind is load-bearing, not decorative. It is the config domain the
// PLATFORM reads the current object from (to stamp the version onto the render
// request) and writes the normalized object back to on submit. A solution that
// declares a screen therefore declares, in the same breath, exactly which
// configuration object it is allowed to shape — and it still never holds the pen.
type ScreenDescriptor struct {
	// Point is WHERE the screen mounts (e.g. ScreenPointWorkspaceSettings).
	// REQUIRED — there is deliberately no default: a screen with no point has
	// no place to appear, and defaulting one would silently mount a partner's
	// UI somewhere the author never chose. An EMPTY or UNKNOWN point causes the
	// platform to DROP the declaration at announce: the tab never appears, the
	// subject is never granted, and the rest of the solution announces
	// normally (a bad screen greys out a screen, never the solution).
	Point string `json:"point"`

	// Name is the screen slug, unique within the solution+point. It is the
	// trailing subject token (`ui.<solution>.<point>.<name>`) and the tab's URL
	// segment, so it must be a bare kebab-case slug.
	Name string `json:"name"`

	// Title is the operator-facing tab label. Falls back to Name when empty.
	Title string `json:"title,omitempty"`

	// ObjectKind is the config domain of the object this screen edits (e.g.
	// "advisory_scope" → `etc/advisory_scope/<workspace>.yaml`). Required for
	// the workspace.settings point: a screen with no target object has nothing
	// for the platform to persist, and nothing to fall back to when the
	// solution is offline.
	ObjectKind string `json:"object_kind"`

	// Icon is an optional riclib/icon kebab name for the tab row.
	Icon string `json:"icon,omitempty"`
}

// Valid reports whether the descriptor is structurally well-formed. An invalid
// descriptor is dropped at declaration time (the tab never appears) rather than
// failing at render — the same fail-early posture as the announce path's
// per-artifact guards.
//
// It deliberately does NOT check ScreenPointKnown: a forward point declared by a
// solution on a newer SDK is well-formed, and it is the platform's job to drop
// what it cannot serve.
func (d ScreenDescriptor) Valid() bool {
	return d.Point != "" && d.Name != "" && d.ObjectKind != ""
}

// Label is the tab label — Title, falling back to Name.
func (d ScreenDescriptor) Label() string {
	if d.Title != "" {
		return d.Title
	}
	return d.Name
}

// ScreenRequest is the envelope the platform sends. It is an HTTP request
// reduced to what a config screen can possibly need: a method, a path, form
// values, and the platform context the solution is NOT allowed to infer for
// itself.
type ScreenRequest struct {
	Verb   ScreenVerb `json:"verb"`
	Method string     `json:"method"`
	Path   string     `json:"path"`

	// Form carries the submitted (or query) values. Encoded as a plain
	// map-of-slices so it round-trips through JSON as url.Values does in Go.
	Form map[string][]string `json:"form,omitempty"`

	Context ScreenContext `json:"context"`
}

// ScreenContext is the platform-asserted half of the envelope. Every field here
// is stamped by the platform from the authenticated request — the solution reads
// it, never sets it. A solution that wants to know who is asking must trust this
// block, because it has no other channel to find out (there is no ambient state
// across the bus and no read-back channel into the platform's config).
type ScreenContext struct {
	// Solution + Screen + Point mirror the subject tokens. Carried in the
	// payload as well so the responder can reject a mismatch — the same
	// advisory identity check the store-proxy wire does on
	// StoreCallRequest.Solution.
	Solution string `json:"solution"`
	Screen   string `json:"screen"`
	Point    string `json:"point"`

	// Workspace is the workspace slug the screen is rendering inside.
	Workspace string `json:"workspace"`

	// UserRole is the caller's resolved workspace role (viewer / full_user /
	// admin / owner). The platform has already gated the route; this is for the
	// solution to shade its own UI, not to make the access decision.
	UserRole string `json:"user_role"`

	// ObjectID is the id of the target object (the workspace slug at the
	// workspace.settings point — one object per workspace per screen).
	ObjectID string `json:"object_id"`

	// ObjectVersion is the version the platform loaded the object at, echoed
	// back on submit. This is `data-original` promoted to a distributed system:
	// if the object moved underneath the editing session, the platform refuses
	// the write rather than clobbering it. Empty means "no object yet" (a
	// create).
	ObjectVersion string `json:"object_version,omitempty"`

	// Object is the current object as the platform holds it, so the solution can
	// render the form without a read-back channel. This is the whole of the
	// solution's view of its own configuration.
	Object map[string]any `json:"object,omitempty"`
}

// ScreenReply is the solution's answer.
//
// The two success shapes are deliberately asymmetric. A render replies with
// HTML. A submit replies with EITHER HTML (a re-render carrying inline
// validation errors — banner at 200, no redirect) OR a normalized Object the
// platform then persists. A submit that returns both is read as "persist the
// object, then show this HTML"; a submit that returns neither is an error.
type ScreenReply struct {
	// Status is the HTTP status the solution's handler wrote. 200 for both the
	// happy path and an inline-error re-render (a refused save is a 200 with the
	// errors rendered, never a 4xx the browser swallows).
	Status int `json:"status"`

	// HTML is the fragment to render into the tab. Subject to the platform's
	// allowlist sanitizer before it is ever emitted — see `docs/screens.md` for
	// what a fragment must survive.
	HTML string `json:"html,omitempty"`

	// Object is the validated, normalized object the PLATFORM writes. Set only
	// on a successful submit. The solution never writes configuration — this
	// field is the entire persistence contract.
	Object map[string]any `json:"object,omitempty"`

	// Error is a solution-level failure message (as opposed to a transport
	// failure, which surfaces as a Go error on the platform's client).
	// Non-empty means the exchange reached the solution and it declined.
	Error string `json:"error,omitempty"`
}
