import json, sys

R = lambda name: {"$ref": f"#/$defs/{name}"}
S = {"type": "string"}
B = {"type": "boolean"}
I = {"type": "integer"}
MS = R("ms")
REMAINING = {"type": "integer", "minimum": 0, "description": "Remaining time in ms (clock maximum minus elapsed), as shown to the operator"}
NULLABLE_ID = {"oneOf": [R("id"), {"type": "null"}]}

# type -> (required payload fields, optional payload fields, required clocks)
EVENTS = {
    # setup
    "GameCreated": ({"game": R("id"), "ruleset": R("ruleset")}, {"name": S, "info": R("info"), "timezone": R("timezone"), "gameType": R("gameType"), "cueMode": R("cueMode")}, []),
    "GameInfoUpdated": ({}, {"info": R("info"), "timezone": R("timezone"), "gameType": R("gameType"), "cueMode": R("cueMode")}, []),
    "RulesChanged": ({"overrides": R("ruleOverrides")}, {}, []),
    "TeamSet": ({"team": R("team"), "name": S}, "TEAMFIELDS", []),
    "TeamUpdated": ({"team": R("team")}, "TEAMFIELDS+name", []),
    "SkaterAdded": ({"team": R("team"), "skater": R("id"), "number": S}, {"name": S, "pronouns": S, "pronunciation": S, "wuid": S, "flags": R("flags"), "status": R("skaterStatus")}, []),
    "SkaterUpdated": ({"skater": R("id")}, {"number": S, "name": S, "pronouns": S, "pronunciation": S, "wuid": S, "flags": R("flags"), "status": R("skaterStatus")}, []),
    "SkaterRemoved": ({"skater": R("id")}, {}, []),
    "SkaterReplaced": ({"skater": R("id"), "by": R("id")}, {}, []),
    "StaffAdded": ({"team": R("team"), "staff": R("id"), "name": S}, {"role": S, "flags": R("flags")}, []),
    "StaffUpdated": ({"staff": R("id")}, {"name": S, "role": S, "flags": R("flags")}, []),
    "StaffRemoved": ({"staff": R("id")}, {}, []),
    "OfficialAssigned": ({"official": R("id"), "name": S, "role": S}, {"league": S, "cert": S, "p1Team": R("team"), "swap": B}, []),
    "OfficialUpdated": ({"official": R("id")}, {"name": S, "role": S, "league": S, "cert": S, "p1Team": R("team"), "swap": B}, []),
    "OfficialRemoved": ({"official": R("id")}, {}, []),
    # game flow
    "PeriodStarted": ({"period": R("id"), "number": {"type": "integer", "minimum": 1}}, {}, ["pc"]),
    "JamUpcoming": ({"jam": R("id"), "number": {"type": "integer", "minimum": 1}}, {}, []),
    "JamStarted": ({"period": R("id"), "jam": R("id"), "number": {"type": "integer", "minimum": 1}}, {"overtime": B, "injuryContinuation": B}, ["pc"]),
    "JamEnded": ({"jam": R("id"), "reason": {"enum": ["time", "calloff", "injury", "official", "unknown"]}}, {"detail": S}, ["pc", "jc"]),
    "JamUpdated": ({"jam": R("id")}, {"reason": {"enum": ["time", "calloff", "injury", "official", "unknown"]}, "detail": S, "duration": MS}, []),
    "TimeoutStarted": ({"timeout": R("id"), "afterJam": R("id")}, {"owner": R("owner"), "review": B, "asTimeout": B}, ["pc"]),
    "TimeoutUpdated": ({"timeout": R("id")}, {"owner": R("owner"), "review": B, "asTimeout": B}, []),
    "TimeoutEnded": ({"timeout": R("id"), "duration": MS}, {}, ["pc"]),
    "OfficialReviewResolved": ({"timeout": R("id"), "retained": B}, {"request": S, "result": S}, []),
    "PeriodEnded": ({"period": R("id")}, {}, ["pc"]),
    "OvertimeStarted": ({}, {}, []),
    "LineupStarted": ({}, {}, []),
    "SuddenScoringSet": ({"period": R("id"), "value": B}, {}, []),
    "ClockSet": ({"clock": {"enum": ["period", "jam", "lineup", "timeout", "intermission"]}, "time": REMAINING}, {"number": I}, []),
    "ScoreDeclaredOfficial": ({"score": R("score")}, {"hr": R("id"), "hnso": R("id")}, []),
    "GameEndedEarly": ({"outcome": {"enum": ["cancelled", "forfeit"]}, "reason": S}, {"forfeitingTeam": R("team")}, ["pc"]),
    "Undone": ({"reverts": {"type": "array", "items": {"type": "integer", "minimum": 1}, "minItems": 1}},
               {"clocks": {"type": "object", "additionalProperties": False,
                           "properties": {k: REMAINING for k in ["period", "jam", "lineup", "timeout", "intermission"]}}}, []),
    # scoring
    "JamFlagSet": ({"jam": R("id"), "team": R("team"), "flag": {"enum": ["lead", "lost", "calloff", "noPivot"]}, "value": B}, {}, ["jc"]),
    "TripStarted": ({"jam": R("id"), "team": R("team"), "trip": R("id")}, {"afterStarPass": B}, ["jc"]),
    "TripPointsSet": ({"trip": R("id"), "points": {"type": "integer", "minimum": 0}}, {"jc": MS}, []),
    "TripInserted": ({"before": R("id"), "trip": R("id")}, {}, []),
    "TripRemoved": ({"trip": R("id")}, {}, []),
    "StarPassSet": ({"jam": R("id"), "team": R("team"), "trip": NULLABLE_ID}, {}, ["jc"]),
    "OsOffsetSet": ({"jam": R("id"), "team": R("team"), "offset": I}, {"reason": S}, []),
    "ScoreAdjustmentRecorded": ({"adjustment": R("id"), "team": R("team"), "amount": I, "jam": R("id"), "duringJam": B}, {"lastTwoMinutes": B}, []),
    "ScoreAdjustmentApplied": ({"adjustment": R("id"), "trip": R("id")}, {}, []),
    "ScoreAdjustmentDiscarded": ({"adjustment": R("id")}, {}, []),
    # lineups
    "FieldingSet": ({"jam": R("id"), "team": R("team"), "position": R("position")}, {"skater": NULLABLE_ID, "notFielded": B, "sitFor3": B, "boxTrip": R("id")}, []),
    "LineupCopied": ({"fromJam": R("id"), "toJam": R("id"), "team": R("team")}, {}, []),
    # penalties
    "PenaltyIssued": ({"penalty": R("id"), "code": S, "jam": R("id")},
                      {"skater": R("id"), "staff": R("id"), "slot": {"type": "integer", "minimum": 1, "maximum": 9},
                       "calledBy": R("id"), "callingPosition": S}, ["pc", "jc"]),
    "PenaltyUpdated": ({"penalty": R("id")}, {"code": S, "jam": R("id"), "calledBy": R("id"), "callingPosition": S}, []),
    "PenaltyRemoved": ({"penalty": R("id")}, {}, []),
    "PenaltyForceServed": ({"penalty": R("id"), "value": B}, {}, []),
    "ExpulsionRecorded": ({"penalty": R("id"), "info": S, "suspension": B}, {"extraInfo": S}, []),
    "SuspensionServed": ({"skater": R("id")}, {"game": S}, []),
    # box
    "BoxTripStarted": ({"boxTrip": R("id"), "team": R("team"), "jammer": B, "jam": R("id"), "betweenJams": B, "afterStarPass": B},
                       {"skater": R("id"), "position": R("position")}, ["pc", "jc"]),
    "BoxTripEnded": ({"boxTrip": R("id"), "jam": R("id"), "betweenJams": B, "afterStarPass": B}, {"duration": MS}, ["pc", "jc"]),
    "BoxTripMoved": ({"boxTrip": R("id")}, {"start": R("boxPoint"), "end": R("boxPoint")}, []),
    "BoxTripReopened": ({"boxTrip": R("id")}, {}, []),
    "BoxTripDeleted": ({"boxTrip": R("id")}, {}, []),
    "BoxTripPenaltyLinked": ({"boxTrip": R("id"), "penalty": R("id"), "value": B}, {}, []),
    "BoxTripShortened": ({"boxTrip": R("id"), "amount": I}, {"time": MS}, []),
    # structure
    "JamInserted": ({"jam": R("id"), "before": R("id")}, {}, []),
    "JamDeleted": ({"jam": R("id")}, {}, []),
    "TimeoutInserted": ({"timeout": R("id"), "afterJam": R("id")}, {"owner": R("owner"), "review": B, "asTimeout": B, "duration": MS}, []),
    "TimeoutDeleted": ({"timeout": R("id")}, {}, []),
    "PeriodDeleted": ({"period": R("id")}, {}, []),
    # annotations / checkpoints
    "Annotated": ({"target": {"type": "object", "additionalProperties": False, "required": ["kind", "id"],
                              "properties": {"kind": {"enum": ["trip", "fielding", "penalty", "boxTrip", "teamJamSk", "teamJamLt"]},
                                             "id": {"type": "string", "minLength": 1}}},
                   "text": S}, {}, []),
    "HintDismissed": ({"check": S, "target": {"type": "object", "additionalProperties": False, "required": ["kind"],
                                             "properties": {"kind": S, "id": S}}, "reason": S}, {}, []),
    "Checkpoint": ({"summarySha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"}}, {}, []),
}

TEAMFIELDS = {"preparedTeam": R("id"), "fullName": S, "league": S, "teamName": S, "initials": S,
              "nameCue": S, "nameCuePronunciation": S,
              "uniformColor": S, "colors": {"type": "object", "additionalProperties": S},
              "alternateNames": {"type": "object", "additionalProperties": S}, "logo": S}

ENVELOPE = {
    "v": {"const": 1},
    "seq": {"type": "integer", "minimum": 1},
    "t": {"type": "string", "format": "date-time"},
    "type": S,
    "pc": MS,
    "jc": MS,
    "src": {"type": "object", "additionalProperties": False,
            "properties": {"op": S, "device": S, "role": S, "official": S}},
}

# Extra constraints per event type, merged into its schema.
EXTRA = {
    "PenaltyIssued": {"oneOf": [{"required": ["skater"]}, {"required": ["staff"]}]},
    "GameEndedEarly": {"if": {"properties": {"outcome": {"const": "forfeit"}}},
                       "then": {"required": ["forfeitingTeam"]},
                       "else": {"not": {"required": ["forfeitingTeam"]}}},
}

variants = []
for name, (req, opt, clocks) in EVENTS.items():
    if opt == "TEAMFIELDS":
        opt = TEAMFIELDS
    elif opt == "TEAMFIELDS+name":
        opt = dict(TEAMFIELDS, name=S)
    props = {k: {} for k in ENVELOPE}
    props["type"] = {"const": name}
    props.update(req)
    props.update(opt)
    variants.append({
        "title": name,
        "type": "object",
        "required": ["v", "seq", "t", "type"] + clocks + list(req),
        "properties": props,
        "additionalProperties": False,
        **EXTRA.get(name, {}),
    })

schema = {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "$id": "https://github.com/rollerderby/scoreboard/schema/events-1.json",
    "title": "CRG game event (one line of <game>.events.jsonl)",
    "description": "Draft v1. See README.md for the meaning of each event.",
    "type": "object",
    "properties": ENVELOPE,
    "required": ["v", "seq", "t", "type"],
    "oneOf": variants,
    "$defs": {
        "id": {"type": "string", "pattern": "^[A-Za-z0-9_./-]{1,100}$"},
        "team": {"enum": ["1", "2"]},
        "owner": {"enum": ["1", "2", "O", ""]},
        "ms": {"type": "integer", "minimum": 0, "description": "Milliseconds"},
        "position": {"enum": ["Jammer", "Pivot", "Blocker1", "Blocker2", "Blocker3"]},
        "timezone": {"type": "string", "pattern": "^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$", "description": "IANA time zone, e.g. Europe/Paris"},
        "cueMode": {"enum": ["color", "name"], "description": "How officials identify teams in verbal cues: uniform color or Team Name Cue"},
        "gameType": {"enum": ["sanctioned", "regulation", "other"], "description": "WFTDA Sanctioning Policy definitions"},
        "skaterStatus": {"enum": ["eligible", "ineligible", "notInGame"]},
        "flags": {"type": "string", "description": "Space-separated roster flags: C, A, BC, BA, ALT"},
        "score": {"type": "object", "additionalProperties": False, "required": ["1", "2"],
                  "properties": {"1": {"type": "integer"}, "2": {"type": "integer"}}},
        "rules": {"type": "object", "additionalProperties": {"type": ["string", "number", "boolean"]},
                  "description": "Rule id -> value, e.g. \"Period.Number\": 2"},
        "ruleOverrides": {"type": "object", "additionalProperties": {"type": ["string", "number", "boolean", "null"]},
                          "description": "Rule id -> value; null removes an earlier override"},
        "ruleset": {"type": "object", "additionalProperties": False, "required": ["base", "baseVersion", "overrides"],
                    "properties": {"base": {"type": "string", "description": "Built-in ruleset id, e.g. WFTDARuleset"},
                                   "baseVersion": {"type": "string", "description": "Scoreboard release whose rule defaults apply"},
                                   "name": {"type": "string", "description": "Display name, e.g. of a flattened custom ruleset"},
                                   "overrides": {"$ref": "#/$defs/rules"}}},
        "info": {"type": "object", "additionalProperties": {"type": ["string", "null"]},
                 "description": "Free-form game info (venue, tournament, date, ...). null removes a key."},
        "boxPoint": {"type": "object", "additionalProperties": False, "required": ["jam", "betweenJams", "afterStarPass"],
                     "properties": {"jam": {"$ref": "#/$defs/id"}, "betweenJams": B, "afterStarPass": B}},
    },
}

json.dump(schema, sys.stdout, indent=2)
print()
