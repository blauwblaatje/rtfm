# CRG game data format — draft v1

Status: **draft**, for discussion. Derived from the Java model in
`src/com/carolinarollergirls/scoreboard/core/interfaces/` (as of v2027.1).

Each game is stored as two files:

| File | What | Written |
|---|---|---|
| `<game>.events.jsonl` | Append-only **event log**: one JSON object per line, in order | On every event |
| `<game>.json` | Nested **game summary** (`game.schema.json`) | At period end, game end, on request |

The event log is the source of truth. The summary can always be rebuilt by
replaying the log; it exists for humans, the REST API, the statsbook exporter
and third-party tools.

Schemas:

- `events.schema.json` — one line of the event log
- `game.schema.json` — the game summary
- `examples/` — a short sample game in both formats

---

## Licence

GPL-3.0 (see LICENSE), the licence of the Java CRG scoreboard, so its pages and
tools under the same licence (CRG-Stats) can be reused. The server bundles the
Java scoreboard's overlay, whiteboard and roster pages; NOTICE lists them and
the libraries that come with them.

## Design rules

1. **Record facts, not UI actions.** `TripPointsSet {points: 4}` rather than
   "SK pressed +1 four times". Replaying must not depend on which screen was used.
2. **Absolute values, not deltas.** Setting points to 4 twice gives the same
   result as once, so re-applying an event doesn't corrupt the game.
3. **Stable IDs for everything that can be inserted or deleted.** Jams, trips,
   timeouts, penalties and box trips get an ID when created. Jam/trip *numbers*
   are derived from order, because `Jam.InsertBefore`, `Jam.Delete` and
   `ScoringTrip.InsertBefore` renumber them.
4. **Timing events carry clock values.** Replay never re-simulates clocks; it
   reads `pc` (period clock elapsed, ms) and `jc` (jam clock elapsed, ms) from
   the event.
5. **No derived values** in either file: no totals, no `DisplayLead`, no box-trip
   symbols, no penalty counts, no `CurrentTrip`. The one exception is
   `result.officialScore` in the summary, because the *official* score is a declared
   fact (signed off by the HNSO). Loaders must check it against the jam scores.
6. **Corrections are events.** Changing jam 7 during jam 12 is logged as a new
   event pointing at jam 7's IDs, so it shows up in the audit trail.

---

## Event envelope

Every line has these fields; the payload fields sit alongside them.

| Field | Type | Req | Meaning |
|---|---|---|---|
| `v` | int | ✓ | Format version, `1` |
| `seq` | int | ✓ | 1, 2, 3, … with no gaps. A gap means a lost event |
| `t` | string | ✓ | Wall-clock time, RFC 3339 UTC with ms |
| `type` | string | ✓ | Event type, see below |
| `pc` | int ms | timing events | Period clock **elapsed** at this moment |
| `jc` | int ms | timing events | Jam clock **elapsed** at this moment |
| `src` | object | | Who caused it: `{ "op": "SK1", "device": "…", "role": "sk", "official": "o_…" }` (`official`: the roster official who picked themselves at the login) |

IDs are strings: a type prefix plus a random suffix, e.g. `j_7Kq2`. Teams are
always `"1"` or `"2"`, as in the Java code.

Positions: `Jammer`, `Pivot`, `Blocker1`, `Blocker2`, `Blocker3`
(`FloorPosition.java`).

---

## Events

### Setup and metadata

| Type | Payload | Java equivalent |
|---|---|---|
| `GameCreated` | `game`, `name?`, `ruleset {base, baseVersion, name?, overrides}`, `info?`, `timezone?`, `gameType?`, `cueMode?` | `StartNewGame` / new `Game` |
| `GameInfoUpdated` | `info?` (keys set to `null` are removed), `timezone?`, `gameType?`, `cueMode?` | `Game.EventInfo` |

`info` is free-form. The keys the tools use are Java's EventInfo keys (`Venue`,
`City`, `State`, `Tournament`, `GameNo`, `HostLeague`, `Date`, `StartTime`),
and `HeadReferee` and `HeadNSO`: the official ids of the heads for the IGRF
(Java's `Game.HR` and `Game.HNSO`), because the head NSO often also works
another position. The official score's `hr`/`hnso`, when declared, take
precedence; without either, whoever has the role "Head Referee" or "Head
Non-Skating Official" is the head (`derive.Heads`).
| `RulesChanged` | `overrides` (a value of `null` removes that override) | `Game.Rule` |
| `TeamSet` | `team`, `preparedTeam?`, `name`, `fullName?`, `league?`, `teamName?`, `initials?`, `nameCue?`, `nameCuePronunciation?`, `uniformColor?`, `colors?`, `alternateNames?`, `logo?` | `Team` prepared props |
| `TeamUpdated` | `team`, any `TeamSet` fields | `Team` |
| `SkaterAdded` | `team`, `skater`, `number`, `name?`, `pronouns?`, `pronunciation?`, `wuid?`, `flags?`, `status?` | `Team.Skater` (`pronunciation` and `wuid` aren't in Java) |
| `SkaterUpdated` | `skater`, `number?`, `name?`, `pronouns?`, `pronunciation?`, `wuid?`, `flags?`, `status?` | `Skater` |
| `SkaterRemoved` | `skater` | |
| `SkaterReplaced` | `skater`, `by` | — |
| `StaffAdded` | `team`, `staff`, `name`, `role?`, `flags?` | — |
| `StaffUpdated` | `staff`, `name?`, `role?`, `flags?` | — |
| `StaffRemoved` | `staff` | — |
| `OfficialAssigned` | `official`, `name`, `role`, `league?`, `cert?`, `p1Team?`, `swap?` | `Game.Nso` / `Game.Ref` |
| `OfficialUpdated` | `official`, any `OfficialAssigned` fields | `Official` |
| `OfficialRemoved` | `official` | |

`SkaterRemoved` is refused for a skater the game refers to (a penalty, a box
trip or a lineup spot): the paperwork would lose them. When the wrong skater
was recorded (111 on the roster and in the paperwork, but it was 11),
`SkaterReplaced` moves everything recorded for `skater` to `by`, a skater of
the same team, and takes `skater` off the roster. Penalties keep their slot if
`by` has it free, and otherwise get the next free one. To keep a skater's
record but take them off the roster, mark them Not Skating (flag `ALT`)
instead.

#### Rulesets

A game refers to a **built-in** ruleset and lists only the rules that differ
from it:

```json
"ruleset": {
  "base": "WFTDARuleset",
  "baseVersion": "v2027.1",
  "name": "Autumn Cup (20 min periods)",
  "overrides": { "Period.Duration": "20:00" }
}
```

- `base` must be a ruleset shipped with the software (today only
  `WFTDARuleset`, `Rulesets.ROOT_ID`). Custom rulesets are stored per machine
  and are often chained (`Ruleset.Parent`), so a game on another machine
  couldn't resolve them. When a game uses a custom ruleset, the whole chain is
  flattened into `overrides` and its name goes in `name`, for display only.
- `baseVersion` is the scoreboard release whose defaults (`Rule.java`) apply.
  Without it, a later release that changes a default would silently change old
  games when they are replayed. A reader that doesn't know that version must
  refuse to replay rather than guess.
- The effective rules at any point in the log are: the base defaults, then
  `GameCreated.ruleset.overrides`, then every `RulesChanged` so far in order.

`timezone` is the IANA name of the venue's time zone (e.g. `Europe/Paris`).
All times in the log and summary are UTC; the time zone is what turns them back
into the local date and start time the statsbook needs.

`gameType` says which rules for running the game apply, following the WFTDA
Sanctioning Policy: `sanctioned` (a WFTDA-sanctioned game between two charter
teams), `regulation` (played to the WFTDA rules, not sanctioned) or `other`
(another organisation's rules). Some checks depend on it: in a sanctioned game
roster numbers are digits only and the game roster has at most 15 Skaters.

**Name cues.** WFTDA is moving from uniform colors to Team Name Cues in
verbal cues ("Cape – 1211 – Penalty"): tested in 2026, planned as standard
from 1 January 2027 (WFTDA Team Name Cues Test Procedures, September 2026).
`nameCue` is the cue a team uses in this game (1–3 syllables, no colors or
numbers) and `nameCuePronunciation` how to say it. `cueMode` says which the
officials use, `color` or `name`; head officials may switch back to colors
during a game, which is a `GameInfoUpdated` whose time records when. The
procedures suggest the scoreboard show "League/Team (Cue)", and the IGRF
color field "Yellow; Cue: Street Cats".

**Team staff** are the non-skating members of a team: bench staff (2 to 4 per
the Sanctioning Policy) and anyone in a coach box. `role` is free text
(e.g. `Bench Coach`, `Coach Box`). A non-skating Alternate has the `A` flag
(Rules 1.2: the Alternate may be a non-skating participant); the Captain must be
a Skater. Staff can be expelled, which is the statsbook's "Non-Skater
Expulsions" row (Rules 4.5); any other penalty for staff is assessed to the
Captain and recorded on the Captain (Rules 5.4). Java has no team staff.

`status` is `eligible` (the default), `ineligible` or `notInGame`, matching
Java's `Skater.BaseRole` (`Bench`, `Ineligible`, `NotInGame`). A skater who is
on the roster but not skating in this game is `notInGame`.

`flags` uses the existing roster flags (`C`, `A`, `BC`, `BA`, `ALT`), so there is
no separate captain event; `Team.Captain` is derived from them.

### Game flow

| Type | Payload | Clocks | Java equivalent |
|---|---|---|---|
| `PeriodStarted` | `period`, `number` | `pc` | first `StartJam` of a period |
| `JamUpcoming` | `jam`, `number` | | the next jam existing before it starts (`Game.UpcomingJam`) |
| `JamStarted` | `period`, `jam`, `number`, `overtime?`, `injuryContinuation?` | `pc` | `Game.StartJam` |
| `JamEnded` | `jam`, `reason`, `detail?` | `pc`, `jc` |
| `JamUpdated` | `jam`, `reason?`, `detail?`, `duration?` | | corrections after the jam: why it ended, how long it was | `Game.StopJam` / jam clock expiry |
| `TimeoutStarted` | `timeout`, `afterJam`, `owner?`, `review?`, `asTimeout?` | `pc` | `Game.Timeout`, `Team.Timeout`, `Team.OfficialReview`, `Game.OfficialTimeout` |
| `TimeoutUpdated` | `timeout`, `owner?`, `review?`, `asTimeout?` | | changing timeout type after it started |
| `TimeoutEnded` | `timeout`, `duration` | `pc` | `StartJam` / `StopJam` while in timeout |
| `OfficialReviewResolved` | `timeout`, `request?`, `result?`, `retained` | | `Timeout.OrRequest/OrResult/RetainedReview` |
| `PeriodEnded` | `period` | `pc` | period clock expiry after last jam |
| `OvertimeStarted` | | | `Game.StartOvertime` |
| `LineupStarted` | | | `Game.StopJam` labelled "Lineup": the operator starts the lineup clock where nothing else does, before the first jam or in an intermission (which it ends) |
| `SuddenScoringSet` | `period`, `value` | | `Game.InSuddenScoring` (JRDA): the engine sets it when a period starts with a big enough score gap (`Jam.SuddenScoring*` rules), the operator can switch it |
| `ClockSet` | `clock`, `time` (remaining ms), `number?` | | manual clock edits, `Clock.ResetTime` |
| `ScoreDeclaredOfficial` | `score {"1","2"}`, `hr?`, `hnso?` | | `Game.OfficialScore` |
| `GameEndedEarly` | `outcome`, `forfeitingTeam?`, `reason` | `pc` | `Game.AbortReason` |
| `Undone` | `reverts` (list of `seq`), `clocks?` | | `Game.ClockUndo` |

`reason` says why the jam ended (Rules 2.2.2, 5.2):

| `reason` | |
|---|---|
| `time` | the jam clock ran out |
| `calloff` | the Lead Jammer called it off (`JamFlagSet calloff` says which) |
| `injury` | officials called it off for an injury. On the statsbook this is the INJ mark, on both teams' lines |
| `official` | officials called it off for another reason (technical problem, interference, unsafe play, or a jammer sent to the box with no opposing jammer, Rules 4.4.2) |
| `unknown` | not recorded, e.g. in games converted from the Java version when it isn't clear |

`detail` is free text for why officials stopped the jam, mainly for
`official`. The WFTDA Officiating Procedures (2.4, 2.5) list the reasons: an
Official Timeout, injury, technical or equipment problems, a penalty that
impacts the game, spectator interference, an emergency, a hazardous surface,
too many skaters on the track, and fighting.

Injury is a property of the jam, not of a team: the jam is called off for
everyone. Which skater was injured is the `sitFor3` mark on their fielding.

`owner` is `"1"`, `"2"`, `"O"` (official timeout) or `""` (not yet chosen), the
same values as `Timeout.Owner`. `asTimeout` marks an Official Review that the
team chose to use as a 60-second timeout (Rules 1.3.2); such a review is never
retained. Java writes this as the request text "Taken as Team Timeout". Lineup and intermission clock starts are
derived from `JamEnded` / `TimeoutEnded` / `PeriodEnded` and are not logged,
except a lineup the operator starts before the first jam or in an
intermission (`LineupStarted`).

#### The upcoming jam

Lineup trackers enter the next jam's lineup while the current jam is still
running, and a skater who goes to the box between jams is recorded against the
next jam. So the next jam has to exist before it starts. `JamUpcoming` creates
it, outside any period; lineups, box trips and annotations can refer to it.
`JamStarted` with the same `jam` ID then moves it into the period.

- There is at most one upcoming jam. `JamUpcoming` while another jam is
  upcoming is an error; `JamDeleted` removes an upcoming jam.
- `JamStarted` for an ID that isn't upcoming creates the jam directly, but
  only if no other jam is upcoming.
- An upcoming jam can't have a timeout after it or a jam inserted before it.
- The summary shows it as `upcomingJam`, with its lineups.

**Games that end early** (WFTDA Cancellation and Forfeit Policy): `outcome`
is `cancelled` (agreed by both teams and the Head Referee, or decided by the
Head Referee or GTO, e.g. an unsafe track) or `forfeit` (a team refuses to
play, or the Head Referee declares it because a team has five or fewer
eligible skaters; `forfeitingTeam` says which). `reason` describes what
happened. `pc` is the period clock at that moment, the "ending game time" the
IGRF needs; with 75% or more of the game played, the score at that moment
stands. A game can end early before it starts (`pc` 0, no period).

`Undone` does not delete lines. Replay skips the events in `reverts`, then
applies `clocks` if present.

**Clock values in `ClockSet` and `Undone.clocks` are *remaining* time**, i.e.
the clock's maximum minus elapsed (Java `Clock.InvertedTime` on a count-down
clock). That is the number the operator sees and types in, so the log matches
what was entered. Every other clock value in this spec (`pc`, `jc`, durations,
`periodClockStart`, …) is *elapsed* time, because those describe when something
happened. Every clock has a maximum (`Clock.MaximumTime`), so remaining time is
well defined for count-up clocks too. A reader converts with
`elapsed = maximum - remaining`, using the maximum from the ruleset at that
point in the log.

### Scoring

| Type | Payload | Clocks | Java equivalent |
|---|---|---|---|
| `JamFlagSet` | `jam`, `team`, `flag`, `value` | `jc` | `TeamJam.Lead/Lost/Calloff/NoPivot` |
| `TripStarted` | `jam`, `team`, `trip`, `afterStarPass?` | `jc` | `Team.AddTrip` (ends the previous trip at `jc`) |
| `TripPointsSet` | `trip`, `points` | `jc?` | `ScoringTrip.Score` |
| `TripInserted` | `before`, `trip` | | `ScoringTrip.InsertBefore` |
| `TripRemoved` | `trip` | | `ScoringTrip.Remove`, `Team.RemoveTrip` |
| `StarPassSet` | `jam`, `team`, `trip` (`null` = no star pass) | `jc` | `TeamJam.StarPass/StarPassTrip` |
| `OsOffsetSet` | `jam`, `team`, `offset`, `reason?` | | `TeamJam.OsOffset` |
| `ScoreAdjustmentRecorded` | `adjustment`, `team`, `amount`, `jam`, `duringJam`, `lastTwoMinutes?` | | `ScoreAdjustment` |
| `ScoreAdjustmentApplied` | `adjustment`, `trip` | | `ScoreAdjustment.AppliedTo` |
| `ScoreAdjustmentDiscarded` | `adjustment` | | `ScoreAdjustment.Discard` |

`flag` is one of `lead`, `lost`, `calloff`, `noPivot`.

**No initial trip (NI)** is not stored: a jammer completed the initial trip
exactly when a trip 2 exists (Rules 3.2: the next trip starts when the jammer
exits the front of the Engagement Zone). On the statsbook, NI is marked on the
jammer's line if there is no trip 2 or the star was passed during the initial
trip, and on the SP line if the new jammer didn't complete it either. Java
calculates `TeamJam.NoInitial` the same way.

**Trip timing.** A trip ends when the next one starts. The last trip of a
team jam ends when its points are entered, if that carries `jc`, and otherwise
when the jam ends. This is what the Java version records.

**Star pass.** `StarPassSet.trip` is the pivot's first trip: the trip in which
the star was passed, counted from the pivot's side. It and every later trip
are after the star pass (Java: `TeamJam.StarPassTrip`, `ScoringTrip.AfterSP`).
A trip started after it inherits "after star pass" from the trip before.

Trip 1 of each team jam is the initial trip and is created implicitly with
the jam (by `JamUpcoming`, or by `JamStarted` for a jam that wasn't upcoming),
with ID `<jam>/<team>/t1`.

### Lineups

| Type | Payload | Java equivalent |
|---|---|---|
| `FieldingSet` | `jam`, `team`, `position`, `skater?` (`null` clears), `notFielded?`, `sitFor3?`, `boxTrip?` (the skater sits in that box trip for someone else: a substitute) | `Fielding.Skater/NotFielded/SitFor3`, `Position.Clear` |
| `LineupCopied` | `fromJam`, `toJam`, `team` | `TeamJam.CopyLineupToCurrent` |

### Penalties

| Type | Payload | Clocks | Java equivalent |
|---|---|---|---|
| `PenaltyIssued` | `penalty`, `skater` or `staff`, `code`, `jam`, `slot?`, `calledBy?`, `callingPosition?` | `pc`, `jc` | `Skater.Penalty(n)` |
| `PenaltyUpdated` | `penalty`, `code?`, `jam?`, `calledBy?`, `callingPosition?` | | |
| `PenaltyRemoved` | `penalty` | | `Penalty.Remove` |
| `PenaltyForceServed` | `penalty`, `value` | | `Penalty.ForceServed` |
| `ExpulsionRecorded` | `penalty`, `info`, `extraInfo?`, `suspension` | | `Game.Expulsion` |
| `SuspensionServed` | `skater`, `game?` | | `Game.SuspensionsServed` |

`slot` is the penalty's place on the Penalties sheet, `1`–`9`. If omitted, the
next free slot is used. `jam` is the jam the penalty belongs to on the
paperwork, which can be the upcoming jam (e.g. Delay of Game, or an Early Hit
while lining up; StatsBook Manual, Penalties).

Foul-outs and the FO/EXP column are **not stored**:

- A skater fouls out when their penalty count reaches the ruleset's
  `Penalties.NumberToFoulout` (7 in the WFTDA rules, Rules 4.5); the FO entry is
  in the jam of that penalty.
- An expulsion is `ExpulsionRecorded` pointing at the penalty it was issued
  for. That penalty is also in the regular slots (StatsBook Manual: "You must
  also write expulsion penalties in the Penalty/Jam # section"); the EXP entry
  shows its code and jam.

Java stores both as an extra `Skater.Penalty(0)`; conversions leave it out.

A penalty for team staff (`staff` instead of `skater`) is the penalty an
expulsion of that staff member was issued for.

### Penalty box

| Type | Payload | Clocks | Java equivalent |
|---|---|---|---|
| `BoxTripStarted` | `boxTrip`, `team`, `skater?`, `position?`, `jammer`, `jam`, `betweenJams`, `afterStarPass` | `pc`, `jc` | `Game.StartBoxTrip`, `Game.StartJammerBoxTrip`, `Fielding.AddBoxTrip` |
| `BoxTripEnded` | `boxTrip`, `jam`, `betweenJams`, `afterStarPass`, `duration?` | `pc`, `jc` | box clock expiry / release |
| `BoxTripMoved` | `boxTrip`, `start?`, `end?` (each `{jam, betweenJams, afterStarPass}`) | | `BoxTrip.StartEarlier/StartLater/EndEarlier/EndLater` |
| `BoxTripReopened` | `boxTrip` | | `Fielding.UnendBoxTrip` |
| `BoxTripDeleted` | `boxTrip` | | `BoxTrip.Delete` |
| `BoxTripPenaltyLinked` | `boxTrip`, `penalty`, `value` | | `BoxTrip.Penalty` |
| `BoxTripShortened` | `boxTrip`, `amount`, `time?` | | `BoxTrip.Shortened`: how many of the trip's penalties were shortened by a jammer swap, and (`time`, ms) how much box time that took off in total |

**Box trip start and end points** use the same convention as the Java code
and the lineup sheet:

| Point | `betweenJams: false` | `betweenJams: true` |
|---|---|---|
| `start` on jam J | went in during jam J | went in **before** jam J started (between jams). On the lineup sheet this is the `S` in jam J's row |
| `end` on jam J | came out during jam J | came out **after** jam J ended |

`afterStarPass` says whether the point is after the star pass in that jam.

`position` is the lineup position the trip started from, in the start jam.
`skater` is who is sitting: normally the skater at that position, but a
substitute when one serves for an injured, fouled-out or expelled skater
(Rules 4.4.3, 4.5). The lineup symbols of the start jam go on `position`; later
jams on wherever `skater` is fielded.

`duration` is the penalty time served, if the box timer recorded it. Penalty
time only runs during jams (Rules 4.4), so without that it follows from the
start and end points and the jam clock. Java's `BoxTrip.Duration` counts jam
time too, but goes negative for trips that end between jams, so conversions
leave it out.

Penalty `Serving`/`Served` and the lineup sheet symbols (`-`, `+`, `S`, `$`,
`3`, …) are all derived from box trips and are not stored.

### Structure edits

| Type | Payload | Java equivalent |
|---|---|---|
| `JamInserted` | `jam`, `before` | `Jam.InsertBefore`, `Period.InsertBefore` |
| `JamDeleted` | `jam` | `Jam.Delete` |
| `TimeoutInserted` | `timeout`, `afterJam`, `owner?`, `review?`, `asTimeout?`, `duration?` | `Jam.InsertTimeoutAfter`, `Timeout.InsertAfter` |
| `TimeoutDeleted` | `timeout` | `Timeout.Delete` |
| `PeriodDeleted` | `period` | `Period.Delete` |

### Annotations and checkpoints

| Type | Payload | Java equivalent |
|---|---|---|
| `Annotated` | `target {kind, id}`, `text` | `*.Annotation`, `TeamJam.SkAnnotation/LtAnnotation` |
| `HintDismissed` | `check`, `target {kind, id?}`, `reason` | — |
| `Checkpoint` | `summarySha256` | — |

`target.kind` is one of `trip`, `fielding`, `penalty`, `boxTrip`, `teamJamSk`,
`teamJamLt`. For `fielding` the ID is `<jam>/<team>/<position>`; for the two
team-jam kinds it is `<jam>/<team>`.

`HintDismissed` records that someone looked at a paperwork hint (package
`hints`, docs/paperwork-review.md) and decided it's right, e.g. a legitimate
substitution. The summary keeps it in `dismissedHints`, and the hint isn't
shown again for that target.

`Checkpoint` is written whenever the summary file is written. A replay that
produces a summary with a different hash has found a bug.

`summarySha256` is the SHA-256 of the summary **in canonical form**
([RFC 8785](https://www.rfc-editor.org/rfc/rfc8785), JSON Canonicalization
Scheme) **with the `log` member removed**. Canonical form means whitespace and
key order don't affect the hash, so any language can reproduce it. Leaving out
`log` means renaming the log file doesn't change it either. The summary it
covers reflects every event before the checkpoint, so its `log.lastSeq` is the
checkpoint's `seq - 1`. `crgreplay hash <summary>` prints the value.

---

## Design notes

- [Paperwork review in the app](docs/paperwork-review.md): checking and
  correcting the statsbook inside the scoreboard, with hints (first version built).
- [The live game engine](docs/engine.md): clocks, commands, REST API and
  push stream for running a game (design).

## Tools

**Building.** `make` builds the tools for this machine into `bin/`;
`make test` runs vet and the tests; `make dist` builds every platform into
`dist/` as `crg-<version>-<platform>` archives with a `SHA256SUMS`: Linux
(amd64, arm64), Windows (amd64, arm64, zip), macOS (Intel, Apple silicon) and
Raspberry Pi OS (`rpi-arm64` for 64-bit, `rpi-armv7` for 32-bit). One
platform: `make rpi-arm64`. The version is `git describe` (`crgserver
-version`). The GitHub workflow (`.github/workflows/build.yml`) runs the
tests and `make dist` on every push and pull request (archives as a workflow
artifact) and publishes a release for a tag `v*`. The macOS binaries aren't
signed: on first start, right-click → Open, or `xattr -d
com.apple.quarantine crgserver`.

`crgreplay` (Go, in `cmd/crgreplay`) reads an event log and:

```sh
go run ./cmd/crgreplay replay -o game.json game.events.jsonl  # rebuild the summary
go run ./cmd/crgreplay verify game.events.jsonl [game.json]   # schema, seq, checkpoints, and diff
go run ./cmd/crgreplay hash game.json                         # checkpoint hash of a summary
```

`verify` checks every line against `events.schema.json`, checks for gaps in `seq`,
replays the log, checks every `Checkpoint`, and if a summary is given, compares
it with a replay up to that summary's `log.lastSeq`, listing each difference by
path. It exits with status 1 if anything fails.

`crgconvert` (in `cmd/crgconvert`) converts game files from the Java version
(see Old games):

```sh
go run ./cmd/crgconvert [-o DIR] [-v] old-games/*.json
```

For each file it builds the event log, replays it, and compares the result
with the values the Java version calculated. With `-o` it writes
`<name>.events.jsonl` and `<name>.json` to DIR. It exits with status 1 if any
file fails or differs. It knows the rule defaults of v2025.10 and v2027.1
(`legacy/defaults_*.go`, generated from `Rule.java` by
`legacy/gen_defaults.py`) and refuses files from other versions.

`go test ./...` runs the converter over every file in `../old-games/` when
that directory exists, and the charter reader over every WFTDA charter in
`../charters/` (the public charter folder,
https://drive.google.com/drive/folders/1CsUcoehaJLQiQD2bMI0n2YuVWYTOUJ9y,
each sheet downloaded as .xlsx; `manifest.json` lists them), and the
tournament builder over the sanctioning applications in `../sanctioning/`
(with the charters the 2026 Championships application links in
`../sanctioning/charters/`).

`crgreplay hints GAME` lists what looks wrong on the paperwork (package
`hints`: the checks from Statsbook-Tool that still apply to this format, see
[docs/paperwork-review.md](docs/paperwork-review.md)). Hints dismissed with
`HintDismissed` are left out.

`crgstatsbook` (in `cmd/crgstatsbook`) writes and reads WFTDA statsbooks:

```sh
go run ./cmd/crgstatsbook export -template blank.xlsx [-o OUT.xlsx] game.events.jsonl
go run ./cmd/crgstatsbook import [-o DIR] [-v] statsbook.xlsx...  # to an event log and summary
go run ./cmd/crgstatsbook diff game.events.jsonl statsbook.xlsx   # hand corrections on the statsbook
```

`export` fills in a blank statsbook (any template from 2019 on) from the
summary. `import` reads a statsbook into an event log; box trips are rebuilt
from the lineup symbols, and times it can't know are 0. `diff` compares the two
as paperwork facts, so it shows what was corrected on the statsbook by hand.

`crgserver` (in `cmd/crgserver`) runs games live (docs/engine.md). One
binary with every page in it; build it with

```sh
go build -o crgserver ./cmd/crgserver                                   # this machine
GOOS=linux GOARCH=arm64 go build -o crgserver ./cmd/crgserver           # Raspberry Pi 4/5, 64-bit Pi OS
GOOS=linux GOARCH=arm GOARM=7 go build -o crgserver ./cmd/crgserver     # Raspberry Pi, 32-bit Pi OS
```

and run it:

```sh
go run ./cmd/crgserver [-addr :8000] [-data DIR] [-html DIR] [-template blank.xlsx] [-auto-end-jam] [-auto-end-tto]
```

Games are kept in `DIR/games/<id>.events.jsonl` (with `<id>.json`, the
summary, rewritten with a checkpoint at every period end and game end). The API
is under `/api` (`/api/games`, `/api/games/current/actions/start-jam`,
`/api/games/current/stream`, …; the table is in docs/engine.md). `-html` serves a
folder of your own pages at `/`, like the Java scoreboard's `html` folder.
Built in are the operator screens (`/nso/sbo/`, `/nso/sk/`, `/nso/plt/`,
`/nso/pbt/`, `/nso/alt/`), the scoreboard display (`/views/standard/`), game
stats (`/views/stats/`), the team library (`/settings/teams/`, kept in
`DIR/teams/`), and the Java scoreboard's own overlay, penalty whiteboard and
roster display, laid out like the Java version's.

`crgproxy` (in `cmd/crgproxy`) is a read-only relay in front of a
scoreboard, for many displays or for sharing on the internet (docs/engine.md
"Proxy"): `crgproxy -upstream http://192.168.1.10:8000 -addr :8080`.

`crgreview` (in `cmd/crgreview`) is the paperwork review screen:

```sh
go run ./cmd/crgreview [-addr 127.0.0.1:8080] [-template blank.xlsx] game.events.jsonl
```

It shows the game in statsbook layout (score, lineups with box symbols,
penalties, box trips) next to the hints. Every change made there is appended to
the event log as an ordinary event (`src.role` is `review`, `src.op` the name
entered on the page), after checking that the event matches the schema and that
the whole log still replays; a change that fails either is refused and nothing
is written. With `-template` the page offers the statsbook as a download.
API: `GET /api/state`, `POST /api/events` (`{"events": [...], "by": "..."}`),
`GET /api/statsbook.xlsx`, `GET /api/log`.

The schemas are embedded from this directory, so after editing
`gen_events_schema.py`, regenerate `events.schema.json` and rebuild.

---

## Not stored

- **Display/UI state:** five-second warning, `AutoFive`, `FiveIndicator`,
  settings, overlays, key controls. These belong in settings, not game data.
- **Clock ticks.** Only starts, stops and manual changes are logged.
- **Derived values:** team/period/jam totals, `DisplayLead`, `CurrentTrip`,
  penalty counts, box symbols, `OnTrackCount`, `AllBlockersSet`,
  `InTimeout`, timeouts/reviews remaining, `Filename`, `LastFileUpdate`.

## Old games

Existing `html/game-data/json/*.json` files are flat snapshots without their
history. `crgconvert` turns one into a **synthesized event log**: the events
that rebuild the snapshot's state, in game order. The summary then comes from
replaying that log like any other, so there is only one way summaries are made,
and a converted game can be continued or corrected like a live one.

A synthesized log is not the real history:

- `info.importedFrom` and `info.importedVersion` name the source file and the
  version that wrote it.
- Times and clock values come from the snapshot where it has them (jams,
  trips, timeouts, periods). For penalties and box trips it only has wall
  times, so their `pc`/`jc` are worked out from where that time falls in the
  jam and period.
- Corrections, undos and anything else that left no trace are not in it.
- It ends with a `Checkpoint`, so later edits can be told apart from the import.

`crgconvert` also checks the replayed summary against the values the Java
version calculated (scores, jam flags, trips, lineups, penalty counts) and
reports every difference.

## Open questions

1. ~~Should `ClockSet` log the elapsed or the remaining time?~~ **Decided:
   remaining** (see Game flow).
2. ~~Do we keep `Undone`, or log explicit reverse events?~~ **Decided: keep
   `Undone`.** It records that an undo was used, by whom and when, which
   reverse events would hide. Third-party readers only need to skip the
   `seq` numbers it lists.
3. **Parked.** Are officials per game (here) or per crew (`OfficialsCrew`) with
   a reference? Crews look like a setup convenience, not game data.
4. ~~Should the log store the full ruleset or just a reference plus
   overrides?~~ **Decided: reference plus overrides** (see Rulesets).
