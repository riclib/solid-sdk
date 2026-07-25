# Screens — rendering your own configuration UI

**Contract version:** 0.1.0
**Status:** DRAFT (pre-1.0 minors can break)
**Owner ticket:** S-1889 (origin: S-1886 platform spike; announce mapper S-1887/S-1888)
**Wire types:** `contract.ScreenDescriptor` (manifest declaration) +
`contract.ScreenRequest` / `ScreenContext` / `ScreenReply` (the envelope)
**Serve adapter:** `transport.ServeScreen` / `ServeScreens` / `DispatchScreen`

Everything else your solution announces is *declarative*: a dashboard is YAML, a
skill is markdown, a lake is a typed struct. Configuration is the exception —
the knobs your solution needs are yours to shape, and no fixed schema language
draws a good form. So screens invert it: the platform asks **you** to render the
form, you answer with an HTML fragment, and the platform keeps the pen.

That inversion is the whole contract:

> **The solution renders, validates and normalizes. The PLATFORM persists.**

A solution never writes configuration — not through a store call, not through a
side channel, not at all. It hands back a normalized object and the platform
writes it where it (and its audit trail, and its version history) already lives.
That is why a partner-shipped screen is safe to mount inside the operator's own
admin shell.

## The exchange

```
ui.<solution>.<point>.<screen>        core NATS request-reply
```

e.g. `ui.cdl-advisory.workspace.settings.advisory-scope`.

Two verbs, and the pair is closed:

| Verb | The platform sends | You answer with |
|---|---|---|
| `render` | the current object + who is asking | the form fragment (HTML) |
| `submit` | the posted form values | a **normalized object** to persist, OR the form re-rendered with inline errors |

**Core NATS, not JetStream, on purpose.** A render sits in front of a browser
request that is already waiting. A screen you fail to answer must fall back NOW,
not replay later. Nothing in this wire queues, retries or persists.

**The subject IS the authz boundary**, exactly as for tools / runnables / fires /
store calls. At approve time the platform grants your account
`ui.<solution>.>` (`contract.ScreenSubjectPrefix`) — one permission covering
every point and screen you declared. Declaring the screen in your manifest is
what earns the grant: **no declaration, no tab, no subject permission.**

## Declaring a screen

```go
transport.SolutionPublish{
    Name: "cdl-advisory",
    // …
    Screens: []contract.ScreenDescriptor{{
        Point:      contract.ScreenPointWorkspaceSettings, // REQUIRED — where it mounts
        Name:       "advisory-scope",                      // kebab slug; the subject's last token
        Title:      "Advisory Scope",                      // the tab label (falls back to Name)
        ObjectKind: "advisory_scope",                       // the config object you edit
        Icon:       "filter",                               // optional riclib/icon name
    }},
}
```

| Field | JSON | Required | Meaning |
|---|---|---|---|
| `Point` | `point` | **yes** | Where the screen mounts. No default, ever — see the registry below. |
| `Name` | `name` | **yes** | Slug, unique within (solution, point). Trailing subject token + URL segment. |
| `Title` | `title` | no | Operator-facing tab label; falls back to `Name`. |
| `ObjectKind` | `object_kind` | **yes** | The config object kind you edit. The platform reads it to render, writes it on submit. |
| `Icon` | `icon` | no | `riclib/icon` kebab name for the tab row. |

Screens ride the **manifest index**, not a KV leaf (a descriptor is five short
strings; your HTML never travels the announce path — only the render reply).
`PublishSolution` refuses a declaration missing `point`, `name` or `object_kind`
before it reaches the bus.

**`ObjectKind` is load-bearing, not decorative.** It is the config domain the
platform reads the current object from, stamps a version onto, and writes your
normalized object back to. Declaring a screen therefore declares, in the same
breath, exactly which object you are allowed to shape — and you still never hold
the pen.

### An empty or unknown point is DROPPED

There is deliberately no default point: a screen with no point has nowhere to
appear, and guessing one would mount a partner's UI somewhere the author never
chose. If `Point` is empty, or names a point the platform does not serve, the
platform **drops that declaration at announce** — the tab never appears, the
subject is never granted, and the rest of your solution announces unaffected. A
bad screen greys out a screen, never a solution.

The registry is additive-only, so a solution built against a newer SDK may
declare a point an older platform has never opened; that platform drops it. Use
`contract.ScreenPointKnown(point)` as a pre-flight check — the platform makes the
real decision.

## The point registry

A **point** is where in the platform's UI a screen mounts, and each point carries
a contract of its own: what the platform guarantees about the surrounding chrome,
and what it requires of your declaration.

| Point | Status | Contract |
|---|---|---|
| `workspace.settings` (`contract.ScreenPointWorkspaceSettings`) | **OPEN** | A tab on the workspace-admin cockpit. `ObjectKind` **required**; the object is **workspace-grained** (one object per workspace, `ObjectID` = the workspace slug); your fragment renders inside the platform's own `<form>`, and the platform owns the Save button and the write. |
| everything else | RESERVED | Not open. A declaration naming one is dropped. |

Points are named, not free-form, because each one is a promise about chrome the
platform must actually keep. The list grows by decision — when a new surface is
opened, with its own contract row — never by a solution inventing a string.

## Writing the handler

Your screen is an ordinary `http.Handler`. The adapter turns each envelope into a
real `*http.Request` (form values as a urlencoded body on submit, as a query
string on render) and folds your recorded response back into the reply. If you
ever have to think about NATS, subjects, or JSON to draw a form, the adapter has
failed.

```go
func AdvisoryScopeHandler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        sc, ok := transport.ScreenContextFrom(r)   // the ONLY way to learn who is asking
        if !ok {
            http.Error(w, "no screen context", http.StatusBadRequest)
            return
        }

        if r.Method == http.MethodGet {
            render(w, r, formFrom(sc.Object))       // sc.Object = the object as the platform holds it
            return
        }

        _ = r.ParseForm()
        obj, errs := normalize(r)                   // trim / lowercase / dedupe / bound
        if len(errs) > 0 {
            render(w, r, formWithErrors(r, errs))   // inline errors at 200 — see below
            return
        }
        if err := transport.WriteScreenObject(w, obj); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
        }
    })
    return mux
}
```

Serve it from the SAME descriptors you announce, so the served subject and the
declared tab cannot drift:

```go
stop, err := transport.ServeScreens(nc, "cdl-advisory", publish.Screens,
    func(d contract.ScreenDescriptor) http.Handler {
        switch d.Name {
        case "advisory-scope":
            return AdvisoryScopeHandler()
        }
        return nil // declared but unhandled → no responder → the platform falls back
    })
defer stop()
```

`transport.ServeScreen(nc, solution, point, screen, h)` subscribes one screen if
you prefer to wire them by hand, and `transport.DispatchScreen` is the pure
envelope→handler→envelope function underneath both — call it directly in a unit
test and you need no NATS at all.

### What the platform tells you (`ScreenContext`)

Every field is stamped by the platform from the authenticated request. You read
it; you never set it. There is no ambient state across the bus and no read-back
channel into the platform's configuration — this block is the whole of your view.

| Field | JSON | Meaning |
|---|---|---|
| `Solution` / `Screen` / `Point` | `solution`, `screen`, `point` | Mirror the subject tokens. The adapter refuses (403) a payload that disagrees with the subject it arrived on, before your handler runs. |
| `Workspace` | `workspace` | The workspace slug the screen is rendering inside. |
| `UserRole` | `user_role` | The caller's resolved role (`viewer` / `full_user` / `admin` / `owner`). The platform has ALREADY gated the route — use this to shade your UI, not to make the access decision. |
| `ObjectID` | `object_id` | The target object's id (the workspace slug at `workspace.settings`). |
| `ObjectVersion` | `object_version` | The version the platform loaded the object at, echoed back on submit — `data-original` promoted to a distributed system. If the object moved under the editing session, the platform refuses the write rather than clobbering it. Empty = no object yet (a create). |
| `Object` | `object` | The current object as the platform holds it, so you can render the form without a read-back channel. |

### The two success shapes on submit

- **`transport.WriteScreenObject(w, obj)`** — hand back the validated, normalized
  object. The platform writes it. This is the entire persistence contract.
- **Re-render your form with the errors in it** — a normal 200 HTML response.
  That is the refusal path and it needs no special API: no object comes back, so
  the platform has nothing to persist.

They are mutually exclusive by construction — you write to one recorder, and
`WriteScreenObject` claims the content type
(`application/vnd.solid.screen-object+json`).

**A refusal is a 200, not a 4xx.** Errors belong inline, on the offending
fields, in the fragment the operator is looking at. A 4xx is a status the browser
swallows and the operator never sees.

**Validate server-side, always.** There is no client-side validation path to
bypass, by construction: the browser posts to the platform, the platform posts to
you. Normalize while you are there — trim, case-fold, drop empties, dedupe, and
bound anything unbounded — because the object you return is the object that gets
written.

## What your fragment must survive: the sanitizer allowlist

The platform runs an allowlist gate over every fragment before it renders it. The
gate **REJECTS, it does not scrub**: a violating fragment is never rendered at
all — the tab falls back to the platform's generic view of the same object, and
the rejection is logged with the offending tag or attribute named. An accepted
fragment is rendered **verbatim**, never rewritten.

The gate is platform-side and is deliberately NOT in this SDK: it is the
platform's defence against the fragment it just received, and a solution running
it on its own output would prove nothing. So write inside the allowlist by
construction:

**Allowed elements** — ordinary structural + text HTML (`div`, `span`, `p`,
`section`, `article`, `header`, `footer`, `main`, `aside`, `nav`, `h1`–`h6`,
`ul`/`ol`/`li`, `dl`/`dt`/`dd`, `br`, `hr`, `pre`, `code`, `blockquote`,
`strong`, `em`, `b`, `i`, `u`, `small`, `sub`, `sup`, `abbr`, `mark`, `a`),
tables (`table`, `thead`, `tbody`, `tfoot`, `tr`, `th`, `td`, `caption`,
`colgroup`, `col`), and form CONTROLS (`label`, `input`, `textarea`, `select`,
`option`, `optgroup`, `button`, `fieldset`, `legend`, `output`, `datalist`,
`progress`, `meter`).

**Allowed attributes** — the standard control attributes (`class`, `id`, `name`,
`value`, `type`, `placeholder`, `for`, `checked`, `selected`, `disabled`,
`readonly`, `required`, `rows`, `cols`, `min`, `max`, `step`, `maxlength`,
`href`, `title`, `colspan`, `rowspan`, …) plus three whole prefix families:
**`hx-*`** (htmx is available to you), **`data-*`**, and **`aria-*`**.

**Never allowed — and none of these is an allowlist gap to be filled:**

| Rejected | Why |
|---|---|
| `<script>`, `<style>`, `<link>`, `<base>` | Solutions ship no code, styles, or resource imports. |
| `on*` handlers (`onclick`, …) | Same rule, in attribute form. |
| the hyperscript `_` attribute | Same rule again. |
| `style="…"` | Inline styling is a rendering-control vector. Use the platform's class names (`card`, `field`, `field__label`, `field__hint`, `field__error`, `input`) and your screen inherits the operator's theme, light and dark. |
| `<img>`, `<iframe>`, `<svg>`, `<object>`, `<embed>`, `src`/`srcdoc` | No remote or framed resources; v1 ships no solution asset channel for screens. |
| **`<form>`**, `action`, `formaction` | The PLATFORM owns the `<form>` element and the submission target. You render the FIELDS that live inside it. A nested form could point a submit somewhere else. |
| `javascript:` / `vbscript:` / `data:text/html` URL values (in `href` or any allowed attribute) | Code again, smuggled through a URL — checked on every attribute value, whitespace-obfuscation included. |
| a `<!doctype>` in the fragment | You returned a whole page, not a fragment. |

So the shape of a screen fragment is: a `card` wrapper, `field` blocks with
labels, hints and inline errors, ordinary inputs/textareas/selects, `hx-*` if you
want interactivity — and nothing else. That is a genuinely sufficient
configuration surface, and it is small enough that the list grows by decision
rather than by omission.

## When you are offline

If the solution does not answer — process down, too slow, never subscribed,
declared-but-unhandled screen, or an estate booted with no NATS at all — the
platform collapses every one of those into ONE signal and renders **the generic
view of the same object** instead, naming who was supposed to render it. Your
configuration is never hostage to your process: the operator can still read and
edit the object through the platform's own editor while you are down, and your
screen takes over again the moment it answers.

Two consequences worth designing for:

- **Your screen is a nicer view of an object that exists without you.** Keep the
  object shape something a generic YAML editor can present honestly (flat lists
  and scalars beat deep nesting).
- **A malformed reply is NOT a fallback.** A reply the platform cannot parse, or
  a fragment the sanitizer rejects, is a solution bug — it surfaces as one
  instead of quietly degrading.

## Version history

- **0.1.0** (S-1889) — first cut: `ScreenDescriptor` on the manifest, the
  render/submit envelope, the `transport.ServeScreen` adapter, one open point
  (`workspace.settings`). Out of this cut on purpose: arbitrary pages,
  solution-shipped JS/CSS/assets, SSE-over-NATS fragments, any retry or queueing
  machinery.
