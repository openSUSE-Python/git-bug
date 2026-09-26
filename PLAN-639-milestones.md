# Implementation plan: milestone support (issue #639)

Upstream issue: [git-bug/git-bug#639 "Support milestones"](https://github.com/git-bug/git-bug/issues/639),
last updated 2026-09-25. Local branch: `639-milestones`, base `079da949` (v0.11.0+9).
State of the tree audited before this plan was written: labels, statuses, and the
bug/identity entity model, the query DSL, the GraphQL layer, the webui, the
termui, and the GitHub/GitLab/Jira/Launchpad bridges.

## What the issue asks for

The original request (GlancingMind, April 2021) is a milestone type that groups
bugs into a bigger piece of work:

- a milestone has a title and a description
- every bug belongs to exactly one milestone: the one it is assigned to, or
  the unassigned bucket when it has no assignment. The assignment changes
  as plans change; the absence of one is a visible state, not an error and
  not something the schema enforces
- a milestone has an optional due date that is a hint only, with no enforced
  deadline
- milestones can be open or closed, like bugs
- users can see all milestones in a new menu, in the webui and the termui
- completion (closed/total as a percentage) is presentation only; the core
  concept works without it and it can land as a follow-up

The follow-up comments add three constraints worth respecting:

- **Michael Mure (2021, quoted in the thread)**: milestones "should probably
  have their own operation in git-bug's core instead of piggy-backing on the
  labels", and a GraphQL-side workaround is acceptable as an intermediate step
  if the core work is out of scope.
- **sparr (2024)**: label-of-labels ("labels that have labels") would let
  milestones reuse the label code path.
- **andreasabel (2026)**: the GitHub bridge should download milestones from
  GitHub.
- **mcepl (2026)**: the strongest reason to do this is compatibility with the
  bridged trackers, which almost all have milestones. Labels do not cut it:
  "labels are conceptually text and couldn't be compared and ordered".

Taken together, the thread argues for a first-class milestone entity, a real
per-bug membership field, and bridge parity. This plan implements that.

## Design decisions

1. **Milestones are a first-class entity, a sibling of `bug` and `identity`.**
   The existing `entity/dag` framework gives this nearly for free: a new
   `dag.Definition` with `Typename "milestone"` and `Namespace "milestones"`
   yields storage under `refs/milestones/<id>`, push/pull, and merge with
   Lamport clocks. The identity package (`entities/identity`) is the template
   to copy structurally for a non-`bug` entity; `entities/bug` is the
   template for the operation and snapshot code.

2. **Membership is a per-bug operation, not a label and not a composite bug.**
   A new `SetMilestoneOperation` on the bug entity carries the milestone
   name. The bug snapshot gains `Milestone *common.Milestone`, set by that
   operation and cleared by the same type with an empty name.

   Two rejected alternatives, both from the thread:
   - *Composite / milestones-as-bugs* (GlancingMind's closing note in the
     issue body). Nesting a milestone chain of git objects inside each bug
     would couple two DAGs that merge independently, and membership would be
     duplicated in N bugs. It is the wrong shape for this problem.
   - *Label-hierarchies* (sparr). Generic labels of labels is a platform
     feature with real complexity, and mcepl's objection still applies:
     milestones must be comparable and orderable, and a label hierarchy gives
     neither. Labels stay what they are: flat, displayable, filterable text.

   This is the "own operation in the core" path that Michael Mure asked
   for: `SetMilestoneOperation` is a real operation type with its own enum
   value, its own timeline item, and its own merge semantics, modeled on
   `LabelChangeOperation` and `SetStatusOperation`. Copy-paste from the label
   code is the expected cost, and it is the cost Mure already accepted.

3. **The milestone is referenced by name, not by ID.** A bug carries the
   milestone name as a plain string. Cross-repo merge is by name: two
   independent `SetMilestoneOperation`s that carry the same name are
   idempotent, and only a genuinely different name is a real change. Name
   uniqueness inside a repo is enforced at the `cache` layer at create time,
   not by the entity model. Renaming a milestone is deliberately out of scope
   (phase 7 notes how to add it later).

4. **Removing a milestone is a ref unlink, not an operation.** There is no
   delete operation anywhere in the codebase: `git-bug bug rm` calls
   `dag.RemoveAll`/`Remove`, which unlinks `refs/<namespace>/<id>` locally
   (the long form of `bug rm` says it explicitly that the remote copy is
   untouched), and the identity package has no remove path at all. A
   milestone gets the same treatment: `git-bug milestone rm NAME` unlinks
   the local ref. Consequence to handle, not to prevent: bugs that still
   carry the name keep compiling fine with a dangling reference, `milestone
   show` and the `milestone:` filter report the missing name, and the CLI
   warns at removal time when linked bugs exist.

5. **Progress is computed, never stored.** `RepoCacheMilestone.bugs()` + each
   bug's status gives `closed / total`. Nothing is cached, so no code updates
   the numbers when a bug changes state. This is presentation only in the
   sense that the milestone concept does not depend on it; the query-layer
   primitives cost nothing and the webui bar may still ship later.

6. **Due date is inert.** It is stored (`*time.Time`, nil means none), shown
   with an "overdue" style when in the past, and changes nothing else. The
   issue explicitly says the deadline should have no effect, and mcepl's
   ordering point is honored semantically: where a presentation needs an
   order (CLI default list, webui list), milestone names that parse as
   versions are compared with `semver`, the rest fall back to natural string
   order.

7. **Bridge parity via the metadata key of the operation**, not the bug.
   The operation stores `git-bug.gitlab.id` / `git-bug.github.id` in its
   metadata, the same mechanism `SetTitle` and `ChangeLabels` use
   (`metaKeyGitlabId`, `metaKeyGithubId`). On import, a milestone event
   whose remote ID matches an existing operation is skipped exactly like
   labels are, so re-imports are idempotent.

## Data model (phase 1)

New files, all in new/parallel locations mirroring the existing layout:

| File | Copies the shape of | Contents |
| --- | --- | --- |
| `entities/common/milestone.go` | `entities/common/label.go` | `type Milestone string`, `Validate()` (non-empty, `text.SafeOneLine`), and a `common.MilestoneId` alias over `entity.Id` if needed for the bug op |
| `entities/milestone/` | `entities/identity/` package structure | the milestone entity, `Milestone` snapshot type, `CreateOperation` + `EditOperation` (both), `Snapshot` with `Name`, `Description`, `Status`, `DueDate *time.Time`, `Timeline`, ops, and `Validate`; `def = dag.Definition{Typename: "milestone", Namespace: "milestones", ...}`, `formatVersion = 1` |
| `entities/bug/op_set_milestone.go` | `entities/bug/op_label_change.go` | `SetMilestoneOperation`, `MilestoneChangeTimelineItem`, the convenience functions below |
| `entities/bug/op_set_milestone_test.go` | `op_label_change_test.go` | `dag.SerializeRoundTripTest` + apply + validation tests |

### `SetMilestoneOperation`

```go
type SetMilestoneOperation struct {
    dag.OpBase
    Milestone common.Milestone // empty string = unassign
    // metadata holds e.g. metaKeyGitlabId / metaKeyGithubId, mirroring
    // SetTitleOperation and LabelChangeOperation usage
}
```

Registration is one new value in `entities/bug/operation.go`:

```go
SetMilestoneOp // appended after SetMetadataOp in the const block

case SetMilestoneOp:
    op = &SetMilestoneOperation{}
```

Because `dag.UnknownOperation` preserves raw JSON for types an old client
does not know, a 0.11.x client pulling from a milestones-enabled repo keeps
working: it stores the op, `Apply` is a no-op, the bug is shown without a
milestone, and nothing corrupts. The inverse (a new client reading a repo
from before milestones) is the trivial empty case: `Snapshot.Milestone` is
nil. No `formatVersion` bump on the bug is needed for either direction;
verify with `entity/dag/operation_test.go` style tests and one
`git bug pull` round-trip between an old and new binary in the test suite.

`Apply` sets `snapshot.Milestone = &op.Milestone` (or nil if empty) and
appends a `MilestoneChangeTimelineItem` with `Author`, `UnixTime`,
`Milestone` (new value), and `Was` (previous value), the same shape as
`SetTitleTimelineItem`.

Convenience functions, mirroring `ChangeLabels` / `ForceChangeLabels`:

```go
SetMilestone(b Interface, author identity.Interface, unixTime int64,
    name string, metadata map[string]string) (*SetMilestoneOperation, error)
UnsetMilestone(b Interface, author identity.Interface, unixTime int64,
    metadata map[string]string) (*SetMilestoneOperation, error)
SetMilestoneRaw / UnsetMilestoneRaw // the same, author and time given
ForceSetMilestone  // skips dedup, for the bridge importer
```

Dedup rule: if the bug already has exactly `name` set, `SetMilestone`
returns a no-op result (`MilestoneChangeUnchanged`), mirroring
`LabelChangeAlreadySet`.

### Snapshot

`entities/bug/snapshot.go` gains one field:

```go
type Snapshot struct {
    // ...existing fields...
    Milestone *common.Milestone // nil when the bug has no milestone
}
```

`Compile()` already replays every op, so no new compile code beyond the op's
`Apply`. `entities/bug/timeline.go` gains `MilestoneChangeTimelineItem` with
the `IsAuthored()` signpost method other items have.

### The milestone entity

The milestone entity is a small version-chain DAG in the identity style.
Only two operation types, kept deliberately small:

```go
type CreateOperation struct {
    dag.OpBase
    Name        string         `json:"name"`
    Description string         `json:"description"`
    DueDate     *time.Time     `json:"dueDate,omitempty"`
}

type EditOperation struct {
    dag.OpBase
    Name              *string    `json:"name,omitempty"`
    Description       *string    `json:"description,omitempty"`
    DueDate           *time.Time `json:"dueDate,omitempty"` // pointer, nil = clear
    Status            *common.Status `json:"status,omitempty"`
}
```

`DueDate` as a pointer distinguishes "never set" from "cleared". `Name` in
the edit op is present only if renames ship in phase 7; before then it is
absent and the edit op is description/status/due-date only. `Validate()`
enforces one-line name, safe description (`text.Safe`, multiline allowed),
and a valid status when present.

The snapshot accumulates: `Name`, `Description`, `Status` (default open),
`DueDate`, `CreateTime`, `EditTime`, `Actors`, `Timeline`.

### Cache layer

`cache/milestone_cache.go`, `cache/milestone_subcache.go`,
`cache/milestone_excerpt.go` mirror the bug triplet. `RepoCacheMilestone`
exposes:

- `Create(name, description string, dueDate *time.Time) (*Milestone, error)`
  with a duplicate-name check against the subcache
- `ByName(name string) (*Milestone, error)`
- `All(q *query.Query) ([]entity.Id, error)`
- `Get(id entity.Id) (*Milestone, error)`
- `Open(id)`, `Close(id)`
- `Bugs(id) ([]*BugCache, error)` via the repo's bug subcache, matching
  `excerpt.Milestone == name` (filter in the phase-2 excerpt, hand-rolled
  until then)
- `Progress(id) (closed, total int, done bool)` as a single pass over
  `Bugs(id)` counting `excerpt.Status == common.ClosedStatus`; `done` is
  `total > 0 && closed == total`

`RepoCache` gains `Milestones() *RepoCacheMilestone`, wired into
`repo_cache.go` alongside `Bugs()` and `Identities()`.

`BugCache` gains `SetMilestone(name string)`,
`UnsetMilestone()`, and the `*Raw` variants, each calling the entity
function, locking, and `notifyUpdated()` exactly like `ChangeLabels` today.

## Query language and excerpts (phase 2)

`query.Filters` gains two fields:

```go
Milestone []string
NoMilestone bool
```

`query.Parse` handles `milestone:NAME` (appended, so multiple names are OR'd
like `label:`) and `no:milestone`. The `no:` switch in the parser gains the
`"milestone"` case. `query/lexer_test.go` and any parser tests get matching
cases, including quoted names with spaces.

`cache/bug_excerpt.go` gains `Milestone *common.Milestone` populated in
`NewBugExcerpt` from the snapshot. `cache/filter.go` gains
`MilestoneFilter(name)` and a `NoMilestoneFilter()` (true when
`excerpt.Milestone == nil`), wired into `compileMatcher` and `Matcher.Match`
as AND-matches within the milestone group, OR across the provided names
(same semantics as labels).

Bleve full-text search: milestone names are not added to the index as their
own field. Free-text search over title and message keeps working; the
`milestone:` filter is structural. Documenting this in `doc/usage/` is part
of this phase so it reads as a decision, not an oversight.

CLI in this phase:

- `git-bug milestone list` (the new root for the subcommands, registered in
  `commands/root.go` alongside `label.go`), defaulting to open milestones
  with `--all` to include closed, `--json` for machine output, sorted by the
  semantic order from decision 5
- `git-bug milestone` bare (no subcommand) prints the list; `git-bug
  milestone show NAME` prints the milestone, its description, due date
  (with overdue flag), and its bugs with per-bug status
- `git-bug bug show` adds a `milestone:` line when set, in both the
  human-readable and `--json-field` paths (`commands/bug/bug_show.go`),
  with `"milestone"` added to the valid-field list
- `git-bug bug new` and `git-bug bug select` get a `--milestone NAME` flag
  that applies at creation, and `git-bug milestone set BUG_ID NAME`,
  `git-bug milestone unset BUG_ID` use the cache functions above
- `git-bug milestone new NAME [--description TEXT] [--due DATE]` and
  `git-bug milestone close NAME` / `git-bug milestone open NAME`
- `git-bug milestone rm NAME` unlinks the local milestone entity, in the
  exact scope of `git-bug bug rm` (local only, remote untouched per the
  existing `bug rm` long help). It prints a warning listing bugs still
  assigned to the name so the dangling-reference state of decision 4 is
  visible where it is created

Shell completion follows the existing pattern of `commands/bug/completion.go`
(`MilestoneCompletion` helper) and the label command tree.

## TermUI (phase 3)

- The bug table in `termui/bug_table.go` gets a milestone column, shown
  only when at least one visible bug has one (like labels), with a color
  derived from `common.Label.Color()` applied to the milestone name.
- A `milestoneSelect` widget modeled on `termui/label_select.go` opens from
  the bug-show screen (`show_bug.go`) with the same binding label the label
  picker has. It lists open milestones (closed ones under a flag), and
  picking one applies `SetMilestone` / `UnsetMilestone` with a no-op option,
  reusing the same `SetBug` / keybinding / layout structure as
  `labelSelect`.

## GraphQL API (phase 4)

Schema additions, one file each under `api/graphql/schema/`:

`milestone.graphql` (new):

```graphql
type Milestone {
    name: String!
    status: Status!
    description: String
    dueDate: Time
    bugs(after: String, before: String, first: Int, last: Int): BugConnection!
    openBugs: Int!
    closedBugs: Int!
    totalBugs: Int!
    # closedBugs / totalBugs; the payload carries the parts and the client
    # renders the percentage. Doing the division client-side avoids a
    # schema-level scalar for the ratio and an off-by-one on the last bug.
}

type MilestoneConnection { edges: [MilestoneEdge!]!  nodes: [Milestone!]!
    pageInfo: PageInfo!  totalCount: Int! }
type MilestoneEdge { cursor: String!  node: Milestone! }

input SetMilestoneInput { clientMutationId: String  repoRef: String
    bug: String!  milestone: String! }
input UnsetMilestoneInput { clientMutationId: String  repoRef: String  bug: String! }
input MilestoneCreateInput { clientMutationId: String  repoRef: String
    name: String!  description: String  dueDate: Time }
type SetMilestonePayload { clientMutationId: String  bug: Bug!
    operation: BugMilestoneChangeOperation! }
type UnsetMilestonePayload { clientMutationId: String  bug: Bug!
    operation: BugMilestoneChangeOperation! }
type MilestoneCreatePayload { clientMutationId: String  milestone: Milestone! }
```

- `Repository` gains `milestones(after, before, first, last, query: String!): MilestoneConnection!` (query string reuses the same DSL, so webui can filter `status:open` etc.) and `milestone(name: String!): Milestone`.
- `Bug` gains `milestone: Milestone`.
- `Mutation` gains `bugSetMilestone(input: SetMilestoneInput!): SetMilestonePayload!`, `bugUnsetMilestone(...)`, and `milestoneCreate(...)`.
- `bug_operations.graphql` gains `BugMilestoneChangeOperation implements Operation & Authored` with `milestone: String!` and `was: String`.
- `bug_timeline.graphql` gains `BugMilestoneChangeTimelineItem implements BugTimelineItem & Authored`.

Go side: a `models.MilestoneWrapper` (repo + compiled milestone) like
`models.BugWrapper`, resolvers in a new `api/graphql/resolvers/milestone.go`
(registered in `root.go`), a `milestone` resolver on the Bug side in
`bug.go`, and the three mutation resolvers in `mutation.go` following the
label-change resolver down to the shape of the returned payload. `go generate
./api/graphql/...` (gqlgen from `handler.go`) regenerates the graph and
models; `api/graphql/graphql_test.go` pattern adds queries for the new
queries and mutations, including the timeline item.

The query string on `allBugs` already accepts `milestone:` and
`no:milestone` after phase 2, so the webui list filter works with no extra
transport work.

## WebUI (phase 5)

- Regenerate TypeScript with `webui/codegen.ts` (the Makefile `code` target)
  after the schema lands, so `Milestone`, the payloads, and the input types
  exist under `webui/src/__generated__/`.
- New pathless layout route `_milestones.tsx` under `webui/src/routes/$repo/`
  mirroring `_issues.tsx`, preloading a `VALID_MILESTONES_QUERY` (open +
  closed) for the list and any page that needs a milestone picker.
- `issue-list` page: the bug list renders in milestone buckets, so a bug is
  visibly in exactly one place: its milestone, or an unassigned bucket when
  it has none. The filter bar gets a `milestone:` control that rewrites the
  query string the existing `query-utils.ts` already parses.
- `issue-detail` page: the `LabelEditor` beside it gets a `MilestoneEditor`
  (`webui/src/components/bugs/milestone-editor.tsx`) listing open milestones,
  with a no-milestone option, calling `bugSetMilestone` /
  `bugUnsetMilestone`.
- `new-issue` form: a milestone select using the same query, passed as the
  initial value of the bug creation.
- `/milestones` index: one row per milestone with name, status, due date (red
  when past), and a bug count that links through to the filtered issue list.
  The `closedBugs / totalBugs` progress bar is presentation; if the scope
  slips it is the first thing to drop, since the primitives it needs already
  exist and nothing else depends on it. Component + interaction tests follow
  the `label-editor*.test.tsx` pattern.

## Bridges (phase 6)

### GitLab

`bridge/gitlab/event.go` already parses `EventChangedMilestone` (body prefix
`changed milestone to %`) and `EventRemovedMilestone`. The GitLab object
`issue.Milestone *Milestone` and `ProjectMilestoneListOptions` exist in
`gitlab.com/gitlab-org/api/client-go v1.46.0`, so no new dependency is
required.

Importer (`bridge/gitlab/import.go`):

- initial import: read `issue.Milestone`, call
  `gi.ensureMilestone(title)` to get or create the local milestone entity
  (metadata `metaKeyGitlabId`, title, description, due date from the API),
  and apply `ForceSetMilestone` with `metaKeyGitlabId` set to the issue
  object's ID for dedup. The event list in `event.go` already marks the two
  milestone events as recognized, so the `default: unexpected event` error
  disappears as a consequence.
- `EventChangedMilestone`: parse the title from the note body, resolve the
  milestone, apply `ForceSetMilestone` with `metaKeyGitlabId` set to the
  note event's `event.ID()`, the same dedup mechanism `EventAddLabel` uses
  with `b.ResolveOperationWithMetadata`.
- `EventRemovedMilestone`: `ForceUnsetMilestone`, same pattern.

Exporter: GitLab's `UpdateIssueOptions.Milestone *string` is enough. If the
local repo assigns a bug to a milestone and the project has one with the
same name, use it. If it does not, the exporter creates it on the remote
side first, matching the label-creation pattern in
`bridge/github/export.go` (`createGithubLabel`) rather than failing or
silently dropping the assignment.

### GitHub

The importer's `issueNode` (in `bridge/github/import_query.go`) needs the
`milestone { title }` field on it. The timeline item union in
`import_events.go` gains the two milestone events; the GraphQL schema for
them carries no milestone title, so for assignment events the importer
re-reads the assigned issue's `milestone.title` on the way in (the issue
node already carries it) and falls back to a best-effort title from the
note body if needed, storing `metaKeyGithubId: item.<Event>.Id` for dedup
exactly like `LabeledEvent` does. `UnassignedFromMilestoneEvent` maps to
`ForceUnsetMilestone` with the same metadata key.

Exporter (`bridge/github/export_mutation.go`): the update-issue mutation
gains a `milestone: title` argument when the local snapshot has one, using
the cached `getMilestoneTitle` / a create call like `createGithubLabel` does
for labels when the remote has no milestone with that title yet.

### Jira and Launchpad

Out of scope for this landing. The entity and cache layers are bridge-free,
so a Jira sprint or Launchpad milestone map is additive later. The plan
records this as the explicit next bridge to pick up, not as a loose thread.

## Docs and meta (phase 7)

- `doc/feature-matrix.md`: milestone row, import/export per bridge, in the
  existing ✅/🟠/❌ layout.
- `doc/design/data-model.md`: the milestone entity and the bug-makes-no-
  reference design, a short section next to the bug and identity sections.
- `doc/usage/query-language.md`: the `milestone:` and `no:milestone` rows in
  the filter table.
- Regenerate the command man pages under `doc/md/` after the CLI lands, so
  the new `milestone` commands have their pages like every other one.
- Renaming a milestone (the one missing operation type): add an optional
  `name` to the edit op, keep the dedup on the name, and note that renaming
  is a repo-local operation; cross-repo merge of renames is out of scope for
  this feature.

## Test surface

- `entities/bug/op_set_milestone_test.go`: `dag.SerializeRoundTripTest`
  (the label op's three cases are the template), apply, validation,
  no-op-dedup, and a clear-then-set round-trip.
- `entities/milestone/*_test.go`: serialize round-trips for both op types,
  `Validate`, `Compile`, and one merge test in the pattern of
  `entity/dag/entity_test.go`.
- `cache/` milestone subcache: name uniqueness, dangling-name behaviour
  after `milestone rm` (bugs still compile, filter reports the miss),
  `Progress` arithmetic on a fixture repo with known open/closed ratios,
  `Bugs` filter.
- `query/lexer_test.go` + `query/parser_test.go`: new qualifier cases.
- `cache/filter_test.go`: `MilestoneFilter` / `NoMilestoneFilter` against a
  fixture excerpt set.
- CLI: `commands/cmdtest` integration cases in the style of the existing
  label command tests (list, create, assign, unassign, show, `--json`).
- GraphQL: new queries and the mutations in `graphql_test.go`, plus one
  subscription/invalidation check that the bug's milestone field updates
  when the op is applied.
- Webui: component and interaction tests for the editor and the list, same
  pattern as `label-editor.interaction.test.tsx`.
- Bridge: gitlab event parser already has tests; add cases for the new
  importer/exporter paths in `import_test.go` / `export_test.go` style with
  the existing mock clients.
- Compatibility: one `git bug pull` test between an old binary fixture and a
  new one, asserting that an unknown op type does not break the bug.

## Phasing and order

| Phase | Deliverable | Depends on |
| --- | --- | --- |
| 1 | `entities/common.Milestone`, the milestone entity, `SetMilestoneOperation`, snapshot and cache wiring | nothing |
| 2 | query DSL filters, excerpts, filters, CLI commands | 1 |
| 3 | termui column + picker | 1 |
| 4 | GraphQL schema, resolvers, regeneration, API tests | 1 |
| 5 | webui editor, milestone page, list filter | 4 |
| 6 | GitLab importer + exporter, GitHub importer + exporter, Jira/Launchpad documented as later | 1, 4 |
| 7 | docs, man pages, rename op, feature-matrix | 1-6 |

Each phase is a separate commit or small set of commits, in the repo's
conventional-commit style, with the reason stated in the body as with the
existing history.

## Open questions left for review

- Should `milestone set` and `bug new --milestone` accept a prefix for the
  milestone name the way bug IDs are resolved? Current plan: full name only,
  prefix added trivially later if wanted.
- The `bugs` field on the milestone type and the `openBugs` / `closedBugs`
  counts on it mean two ways to render the same data. The plan keeps both;
  if the counts ever become expensive on a large repo, `openBugs` and
  `closedBugs` can be dropped in favor of `totalBugs` + a query string.
- Milestone description: single block (current plan, safe across the
  `text.Safe` boundary) versus full bug-style comments. Comments on
  milestones are not asked for in the issue and are not part of this plan.
