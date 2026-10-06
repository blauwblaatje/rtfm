# RTFM: Roller derby Tournament Fixture Maker

A website that prepares a roller derby tournament's paperwork. Paste the
link to a WFTDA tournament sanctioning application, and optionally the
officials' infopack; RTFM reads the schedule and every team's charter, and
makes each game as it is before the first whistle:

- a **WFTDA statsbook** (.xlsx) with the IGRF filled in: venue, city, game
  number, tournament, host league, date and start time, both teams with
  their uniform colours and charter rosters, and the officials of the
  game's crew;
- a **game file for the CRG scoreboard** (Java, v2025.10): Settings, Data
  Management, Import JSON. One file can hold every game.

**Crews** can be edited on the tournament page: every official's position,
name, pronouns, league affiliation, certification and head mark, and new
officials and crews. League, pronouns and certification can be filled in
from [WFTDA's roster of certified officials](https://resources.wftda.org/officiating/roller-derby-certification-program-for-officials/roster-of-certified-officials/)
(matched by name, ignoring case, accents and punctuation; Non-Skating levels
for NSO positions, Skating levels for referees; a name that's on the roster
twice is left for you), or from a list of your own: an .xlsx or .csv with a
name column and league and/or certification columns, found by their
headings. What's filled in already stays, unless you tick "replace". WFTDA's
site sometimes turns automatic requests away: then save the roster page in
a browser and upload it. The edits are kept with the tournament, so every
download, and everyone who opens it, gets them.

Games whose teams depend on results ("Winner Game 3") get a team picker;
every game gets a crew picker, set to the crew the infopack assigned. The
choices are kept in the browser. *Download all* gives a zip with a statsbook
per game whose teams are known, and one CRG file with all of them.

## How it reads the sheets

The reading is the CRG Go rewrite's (`../crg-format`, vendored in
`vendor/crgformat`):

| Input | Package | What RTFM takes |
|---|---|---|
| Sanctioning application | `tournament` | name, dates, venue, host league, teams with charter links, the schedule (date, time, track, home/away, colours, "Winner/Loser Game N") |
| Charters | `library` | league, team, uniform colours; per skater number, name, pronouns, pronunciation, WUID. **Never legal names.** |
| Infopack | `crews` | the crews: officials by position, heads, the games each crew is assigned. **Never contact details.** |
| WFTDA's roster of certified officials | `internal/fixture` | per official league, pronouns, certifications |
| A list of officials (.xlsx, .csv) | `internal/fixture` | name, league, certification, pronouns; **never a legal-name column** |
| The game | `prepare` | its first events, as the scoreboard makes them |
| Statsbook | `statsbook` | a WFTDA blank statsbook with the IGRF filled in |
| CRG file | `javaws` | the Java scoreboard's keys, version v2025.10 |

Google Sheets links must be shared with "anyone with the link". RTFM
downloads them as .xlsx (`/export?format=xlsx`) from the server: a browser
can't fetch Google Sheets from another site, which is also why there's no
WebAssembly in here. Files can be uploaded instead of links.

## Running it

```sh
make run            # bin/rtfm on :4200, blank statsbooks from blank/, data in data/, logs in logs/
```

Settings (flags or environment):

| Flag | Environment | Default | |
|---|---|---|---|
| `-addr` | `RTFM_ADDR` | `:4200` | where to listen |
| `-blank` | `RTFM_BLANK` | `blank` | folder with WFTDA's blank statsbooks; each `.xlsx` is offered by file name (A4, US Letter) |
| `-logs` | `RTFM_LOGS` | `logs` | log folder, see below |
| `-data` | `RTFM_DATA` | | folder to keep loaded tournaments in; empty keeps them in memory only |
| `-keep` | `RTFM_KEEP` | `1440h` | how long a loaded tournament is kept (60 days) |

**Logs.** Everything goes to `logs/rtfm.log` (and standard error); what
concerns one loaded tournament also to `logs/<id>.log`, the id in its
address (`/t/<id>`): reading the application, every charter (how long it
took, or the error), the infopack's crews, every download with the teams,
colours and crew picked, every request that failed with the answer it got,
and errors the page ran into in the browser. `rtfm.log` moves to
`rtfm.log.1` at 10 MB; a tournament's log goes when the tournament does.

WFTDA's blank statsbooks (A4 and US Letter) are in `blank/`, and built into
the container image: see [blank/README.md](blank/README.md).

## Docker

```sh
docker compose up -d     # http://localhost:4200
```

The image is a static binary on distroless, running as a non-root user,
with `/data` as a volume (the tournaments, and the logs in `/data/logs`)
and a health check (`rtfm -health`). The blank
statsbooks are in the image; to use others, mount a folder on `/blank` (see
`compose.yaml`).

## k3s

```sh
make image k3s-import          # build, and load into k3s without a registry
kubectl apply -k deploy/k8s    # namespace rtfm, 1Gi volume, deployment, service, ingress
```

Change the host in `deploy/k8s/ingress.yaml` (k3s's Traefik serves it). For
a registry, set the image in `deploy/k8s/kustomization.yaml` and build both
architectures (a Raspberry Pi node is arm64) with
`make image-multi IMAGE=ghcr.io/<you>/rtfm`. One replica: the loaded
tournaments live on one ReadWriteOnce volume.

## Developing

```sh
make test     # needs ../sanctioning, ../infopacks and ../statsbooks for the full tests; skipped without
make vendor   # after changing ../crg-format
```

The tests load the 2026 WFTDA Championships from saved copies: 16 charters,
23 games, the infopack's crews; the CRG file was checked by importing it
into a running CRG scoreboard v2025.10.

## Licence

GPL-3, as the CRG scoreboard and the code RTFM uses from it.
