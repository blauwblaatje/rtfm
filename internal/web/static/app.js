"use strict";
// Errors in this page go to the server's log for this tournament
// (logs/<id>.log), so they can be looked at afterwards.
function report(message, source, line, column, stack) {
  try {
    const body = JSON.stringify({ message: String(message), source: source || "", line: line || 0, column: column || 0,
      stack: String(stack || ""), page: location.pathname, agent: navigator.userAgent });
    navigator.sendBeacon(`/t/${document.body.dataset.event}/log`, new Blob([body], { type: "application/json" }));
  } catch (e) {}
}
window.addEventListener("error", (e) => report(e.message, e.filename, e.lineno, e.colno, e.error && e.error.stack));
window.addEventListener("unhandledrejection", (e) => report(e.reason && e.reason.message || e.reason, "", 0, 0, e.reason && e.reason.stack));

// The tournament page: the schedule with a team picker for bracket games, a
// uniform colour per side and a crew per game; downloads per game or all.
// Choices are kept on the server (teams see the bracket games they're in);
// ruleset and paper in this browser (localStorage), per tournament.
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const id = document.body.dataset.event;
const storeKey = "rtfm." + id;
let E = null; // the event (EventView)
let oldChoices = {}; // choices this browser kept before they went to the server
let opts = { rules: "", paper: "" };

function load() {
  try { const s = JSON.parse(localStorage.getItem(storeKey) || "{}"); oldChoices = s.choices || {}; opts = { ...opts, ...(s.opts || {}) }; } catch (e) {}
}
function save() {
  try { localStorage.setItem(storeKey, JSON.stringify({ opts })); } catch (e) {}
}

const team = (no) => E.teams.find((t) => t.no === no);
const choice = (g) => g.choice;

function teamSelect(g, side, ch) {
  if (g.fixed[side]) return `<span class="team">${esc(g.sides[side])}</span>`;
  return `<select data-game="${g.no}" data-side="${side}" data-k="team" aria-label="Game ${g.no} ${side ? "away" : "home"} team">
    <option value="0">${esc(g.sides[side] || "pick a team")}</option>
    ${E.teams.map((t) => `<option value="${t.no}" ${ch.teams[side] === t.no ? "selected" : ""}>${esc(t.name)}</option>`).join("")}
  </select>`;
}

function colorInput(g, side, ch) {
  const t = team(ch.teams[side]);
  const list = `c${g.no}_${side}`;
  const l = g.lineups[side];
  const byTeam = side === 0 && l && l.color && !(g.picked && g.stored.colors[0]);
  return `<input class="color" data-game="${g.no}" data-side="${side}" data-k="color" value="${esc(ch.colors[side])}" list="${list}"
      placeholder="colour" aria-label="Game ${g.no} ${side ? "away" : "home"} uniform colour" ${byTeam ? 'title="picked by the team"' : ""}>
    <datalist id="${list}">${(t && t.colors || []).map((c) => `<option value="${esc(c)}">`).join("")}</datalist>`;
}

// What a team said through its link about this game.
function lineup(g, side, ch) {
  if (!ch.teams[side]) return "";
  const l = g.lineups[side];
  if (!l) return `<div class="lineup none">no line-up from the team yet</div>`;
  const bits = [`${l.skaters} skaters`, l.captain ? `captain ${esc(l.captain)}` : "no captain"];
  if (side === 0 && l.color) bits.push(`colour ${esc(l.color)}`);
  return `<div class="lineup" title="saved ${esc(new Date(l.updated).toLocaleString())}">${bits.join(" · ")}</div>`;
}

// The side's bench, once the home team picked it.
function bench(g, side, ch) {
  if (!ch.teams[side]) return "";
  return g.benches[side] ? `<div class="bench">${esc(g.benches[side])} bench</div>` : `<div class="bench none">bench not picked yet</div>`;
}

// Whether the teams can still change their line-ups for the game: by the
// deadline, or opened or closed by the organiser.
function lineupMode(g) {
  const st = g.lineup;
  const note = st.mode !== "auto" ? "" : !st.closes ? "no deadline" : st.open ? `until ${st.closes}` : `closed ${st.closes}`;
  return `<select class="mode" data-game="${g.no}" data-k="mode" aria-label="Game ${g.no}: the teams' line-ups">
    ${[["auto", "by deadline"], ["open", "open"], ["closed", "closed"]].map(([v, t]) =>
      `<option value="${v}" ${st.mode === v ? "selected" : ""}>line-ups ${t}</option>`).join("")}</select>
    ${note ? `<div class="muted small${st.open ? "" : " shut"}">${esc(note)}</div>` : ""}`;
}

function crewSelect(g, ch) {
  if (!E.crews.length) return `<span class="muted">no infopack</span>`;
  return `<select data-game="${g.no}" data-k="crew" aria-label="Game ${g.no} crew"><option value="">no crew</option>
    ${E.crews.map((c) => `<option value="${c.id}" ${ch.crew === c.id ? "selected" : ""}>${esc(c.name)}${c.id === g.default.crew ? " (infopack)" : ""}</option>`).join("")}</select>`;
}

function query(g, ch) {
  const p = new URLSearchParams({ t1: ch.teams[0], t2: ch.teams[1], c1: ch.colors[0], c2: ch.colors[1], crew: ch.crew, rules: opts.rules, paper: opts.paper });
  return p.toString();
}

// The tabs: all games, or a day's. The tab is in the address
// (#day=2026-10-15), so a link can go straight to a day.
let tab = { kind: "all", key: "" };
function readTab() {
  const m = location.hash.match(/^#(day)=(.+)$/);
  tab = m ? { kind: m[1], key: decodeURIComponent(m[2]) } : { kind: "all", key: "" };
}
function tabs() {
  const out = [{ kind: "all", key: "", label: "All games", n: E.games.length }];
  const days = [...new Set(E.games.map((g) => g.date || ""))];
  if (days.length > 1) for (const d of days) out.push({ kind: "day", key: d, label: d ? shortDay(d) : "Date not known", title: d && dayName(d), n: E.games.filter((g) => (g.date || "") === d).length });
  return out;
}
const inTab = (g) => tab.kind === "all" || (g.date || "") === tab.key;
function renderTabs() {
  const all = tabs();
  if (!all.some((t) => t.kind === tab.kind && t.key === tab.key)) tab = { kind: "all", key: "" };
  $("tabs").hidden = all.length < 2;
  $("tabs").innerHTML = all.map((t, i) => `${i && t.kind !== all[i - 1].kind ? `<span class="sep" aria-hidden="true"></span>` : ""}
    <button type="button" class="tab" data-tab="${t.kind}" data-key="${esc(t.key)}" aria-pressed="${t.kind === tab.kind && t.key === tab.key}"
      ${t.title ? `title="${esc(t.title)}"` : ""}>${esc(t.label)} <span class="count">${t.n}</span></button>`).join("");
  const cur = all.find((t) => t.kind === tab.kind && t.key === tab.key);
  const games = E.games.filter(inTab);
  $("allGames").value = tab.kind === "all" ? "" : games.map((g) => g.no).join(",");
  $("allLabel").value = tab.kind === "all" ? "" : cur.title || cur.label;
  $("allButton").textContent = tab.kind === "all" ? "Download all (zip)" : `Download these ${games.length} game${games.length === 1 ? "" : "s"} (zip)`;
  $("allButton").disabled = !games.length;
}
$("tabs").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-tab]");
  if (!b) return;
  tab = { kind: b.dataset.tab, key: b.dataset.key };
  history.replaceState(null, "", tab.kind === "all" ? location.pathname + location.search : `#${tab.kind}=${encodeURIComponent(tab.key)}`);
  render();
});
window.addEventListener("hashchange", () => { readTab(); render(); });

function render() {
  renderTabs();
  const tracks = new Set(E.games.map((g) => g.track)).size;
  const days = {};
  for (const g of E.games.filter(inTab)) (days[g.date || "Date not known"] ||= []).push(g);
  if (!Object.keys(days).length) { $("games").innerHTML = `<p class="muted">No games here.</p>`; return; }
  $("games").innerHTML = Object.entries(days).map(([day, gs]) => `<h2>${esc(dayName(day))}</h2>
    <div class="scroll"><table class="games"><thead><tr><th>Game</th><th>Time</th><th>Home</th><th></th><th>Away</th><th>Crew</th><th>Download</th></tr></thead>
    <tbody>${gs.map((g) => {
      const ch = choice(g);
      const ready = ch.teams[0] && ch.teams[1] && ch.teams[0] !== ch.teams[1];
      const q = query(g, ch);
      return `<tr class="${ready ? "" : "waiting"}">
        <td class="no">${g.no}${tracks > 1 && g.track ? `<div class="muted small">track ${esc(g.track)}</div>` : ""}</td>
        <td>${esc(g.time)}<div>${lineupMode(g)}</div></td>
        <td>${teamSelect(g, 0, ch)}<div>${colorInput(g, 0, ch)}</div>${bench(g, 0, ch)}${lineup(g, 0, ch)}</td>
        <td class="vs">vs</td>
        <td>${teamSelect(g, 1, ch)}<div>${colorInput(g, 1, ch)}</div>${bench(g, 1, ch)}${lineup(g, 1, ch)}</td>
        <td>${crewSelect(g, ch)}</td>
        <td class="dl">${ready ? `<a class="button" href="/t/${id}/game/${g.no}/statsbook.xlsx?${q}" ${E.blanks.length ? "" : 'aria-disabled="true" title="no blank statsbook on this site"'}>Statsbook</a>
          <a class="button" href="/t/${id}/game/${g.no}/java.json?${q}">CRG file</a>` : `<span class="muted small">${ch.teams[0] && ch.teams[0] === ch.teams[1] ? "same team twice" : "pick the teams"}</span>`}</td>
      </tr>${g.notes ? `<tr class="note"><td></td><td colspan="6" class="muted small">${esc(g.notes)}</td></tr>` : ""}`;
    }).join("")}</tbody></table></div>`).join("");
}

function shortDay(d) {
  const t = new Date(d + "T12:00:00");
  return isNaN(t) ? d : t.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
}
function dayName(d) {
  const t = new Date(d + "T12:00:00");
  return isNaN(t) ? d : t.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
}

const linkURL = (t) => t.link ? location.origin + t.link : "";

function renderTeams() {
  $("teams").innerHTML = `<div class="scroll"><table class="list"><thead><tr><th>#</th><th>Team</th><th>Charter</th><th>Skaters</th><th>Colours</th><th>The team's own link</th></tr></thead><tbody>
    ${E.teams.map((t) => `<tr><td>${t.no}</td><td>${esc(t.name)}</td><td>${t.missing ? `<span class="error">${esc(t.missing)}</span>` : esc(t.charter)}</td>
      <td>${t.skaters || ""}</td><td>${esc((t.colors || []).join(", "))}</td>
      <td><div class="linkCell"><input readonly value="${esc(linkURL(t))}" aria-label="${esc(t.name)}'s link">
        <button type="button" class="small" data-copylink="${t.no}">Copy</button>
        <button type="button" class="quiet small" data-newlink="${t.no}" title="the old link stops working">New link</button></div></td></tr>`).join("")}</tbody></table></div>`;
}

$("teams").addEventListener("click", async (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  const t = team(+(b.dataset.copylink || b.dataset.newlink));
  if (b.dataset.copylink) {
    try { await navigator.clipboard.writeText(linkURL(t)); b.textContent = "Copied"; } catch (err) { b.closest("td").querySelector("input").select(); }
    setTimeout(() => { b.textContent = "Copy"; }, 1500);
    return;
  }
  if (!confirm(`Give ${t.name} a new link? The old one stops working.`)) return;
  const r = await fetch(`/t/${id}/teams/${t.no}/link`, { method: "POST" });
  if (!r.ok) { alert(await r.text()); return; }
  t.link = (await r.json()).link;
  renderTeams();
});

// The line-up settings: the benches' names, the deadline, the time zone.
function renderSettings() {
  const st = E.settings;
  $("bench0").value = st.benches[0];
  $("bench1").value = st.benches[1];
  $("closeHours").value = st.closeHours;
  $("timeZone").value = st.timeZone;
  $("zoneNote").textContent = st.zoneGuessed ? `Guessed from the venue's country${st.timeZone === "UTC" ? " (not known: UTC)" : ""}: check it.` : "";
  try { $("zones").innerHTML = Intl.supportedValuesOf("timeZone").map((z) => `<option value="${esc(z)}">`).join(""); } catch (e) {}
}

$("settingsForm").addEventListener("input", () => { settingsEdited = true; });
$("settingsForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = { benches: [$("bench0").value, $("bench1").value], closeHours: +$("closeHours").value, timeZone: $("timeZone").value };
  const r = await fetch(`/t/${id}/settings`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!r.ok) { $("settingsMsg").textContent = await r.text(); return; }
  E.settings = await r.json();
  settingsEdited = false;
  $("settingsMsg").textContent = "Saved.";
  renderSettings();
  refresh();
});

function renderInfo() {
  $("meta").textContent = [E.dates, E.venue, E.host && `hosted by ${E.host}`].filter(Boolean).join(" · ");
  $("rules").innerHTML = E.rulesets.map((r) => `<option ${r === opts.rules ? "selected" : ""}>${esc(r)}</option>`).join("");
  if (!opts.rules) opts.rules = E.rulesets[0];
  $("paper").innerHTML = E.blanks.length ? E.blanks.map((b) => `<option ${b === opts.paper ? "selected" : ""}>${esc(b)}</option>`).join("")
    : `<option value="">none on this site</option>`;
  if (!E.blanks.includes(opts.paper)) opts.paper = E.blanks[0] || "";
  renderTeams();
  renderSettings();
  $("notesBox").hidden = !E.notes.length;
  $("notes").innerHTML = E.notes.map((n) => `<li>${esc(n)}</li>`).join("");
  const missing = Object.keys(E.missing || {}).length;
  if (missing) $("teamsBox").open = true;
}

// The organiser's choice for a game goes to the server; a colour left empty
// is the default (the home team's pick, the schedule's, the charter's).
// putChoice sends a whole choice, patchChoice only what changed (a side's
// team or colour, or the crew), so it doesn't undo what someone else
// changed in the same game meanwhile.
async function putChoice(no, ch) {
  const r = await fetch(`/t/${id}/games/${no}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(ch) });
  if (!r.ok) throw new Error(await r.text());
}
async function patchChoice(no, p) {
  const r = await fetch(`/t/${id}/games/${no}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) });
  if (!r.ok) throw new Error(await r.text());
}

// Someone typing or picking in a part of the page: a poll doesn't redraw it
// under their hands.
function busy(box) {
  const a = document.activeElement;
  return !!a && box.contains(a) && (a.tagName === "SELECT" || a.tagName === "TEXTAREA" || (a.tagName === "INPUT" && a.type !== "checkbox"));
}

// refresh shows the server's games, crews, settings and team links. The
// page polls it (see below), so what other organisers and the teams change
// shows up by itself; after a change of its own it redraws at once.
let lastData = "";
let settingsEdited = false;
async function refresh(polled) {
  const r = await fetch(`/t/${id}/data.json`, { cache: "no-store" });
  if (!r.ok) throw new Error(`the tournament's data: ${r.status}`);
  const text = await r.text();
  if (polled && (text === lastData || busy($("games")))) return;
  lastData = text;
  const d = JSON.parse(text);
  E.games = d.games || [];
  E.crews = d.crews || [];
  E.teams = d.teams || [];
  render();
  renderTeams();
  if (d.settings && !settingsEdited && !busy($("settingsForm"))) { E.settings = d.settings; renderSettings(); }
}

document.addEventListener("change", async (e) => {
  const el = e.target;
  if (el.id === "rules" || el.id === "paper") { opts[el.id] = el.value; save(); return render(); }
  const no = el.dataset.game;
  if (!no) return;
  if (el.dataset.k === "mode") {
    const r = await fetch(`/t/${id}/games/${no}/lineups`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: el.value }) });
    if (!r.ok) alert(`Game ${no}: ${await r.text()}`);
    return refresh();
  }
  const p = { side: +el.dataset.side || 0 };
  if (el.dataset.k === "team") p.team = +el.value;
  if (el.dataset.k === "color") p.color = el.value.trim();
  if (el.dataset.k === "crew") p.crew = el.value;
  try { await patchChoice(no, p); } catch (err) { alert(`Game ${no}: ${err.message}`); }
  refresh();
});

$("reset").addEventListener("click", async () => {
  if (!confirm("Put every game back to its defaults, for everyone? The teams' line-ups stay.")) return;
  await fetch(`/t/${id}/games`, { method: "DELETE" });
  refresh();
});

// Choices this browser kept before they were kept on the server: teams and
// crew go up once, for games nobody picked on the server yet.
async function moveOldChoices() {
  const nos = Object.keys(oldChoices);
  if (!nos.length) return;
  for (const no of nos) {
    const g = E.games.find((x) => String(x.no) === no), ch = oldChoices[no];
    if (!g || g.picked || !ch || !ch.teams) continue;
    if (ch.teams[0] === g.default.teams[0] && ch.teams[1] === g.default.teams[1] && ch.crew === g.default.crew) continue;
    try { await putChoice(no, { teams: ch.teams, colors: ["", ""], crew: ch.crew || "" }); } catch (e) { report(e.message, "moveOldChoices"); }
  }
  oldChoices = {};
  save();
  refresh();
}

// --- crews: edited here, kept on the server --------------------------------------------

let C = []; // the crews being edited
let roles = [];
let dirty = false;

async function loadCrews() {
  const d = await (await fetch(`/t/${id}/crews`)).json();
  C = d.crews || [];
  roles = d.roles || [];
  dirty = false;
  renderCrews();
}

// One crew at a time, a tab per crew; the number on a tab is how many of
// its officials have no league or certification yet.
let crewTab = 0;
const missingInfo = (c) => (c.officials || []).filter((o) => !(o.league && o.cert)).length;
function renderCrewTabs() {
  $("crewTabs").hidden = C.length < 2;
  $("crewTabs").innerHTML = C.map((c, ci) => `<button type="button" class="tab" data-crewtab="${ci}" aria-pressed="${ci === crewTab}"
    title="${(c.officials || []).length} officials, ${missingInfo(c)} without league or certification"><span class="label">${esc(c.name || "(no name)")}</span>
    ${missingInfo(c) ? `<span class="count">${missingInfo(c)}</span>` : ""}</button>`).join("");
}
$("crewTabs").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-crewtab]");
  if (!b) return;
  crewTab = +b.dataset.crewtab;
  renderCrews();
});

function renderCrews() {
  $("saveCrews").disabled = !dirty;
  $("saveCrews").textContent = dirty ? "Save crews" : "Saved";
  crewTab = Math.max(0, Math.min(crewTab, C.length - 1));
  renderCrewTabs();
  if (!C.length) { $("crews").innerHTML = `<p class="muted">No crews yet: load an infopack with the tournament, or make one with New crew.</p>`; return; }
  const field = (ci, oi, k, v, size) => `<input data-crew="${ci}" data-off="${oi}" data-f="${k}" value="${esc(v)}" ${size ? `size="${size}"` : ""} aria-label="${k}">`;
  $("crews").innerHTML = C.map((c, ci) => ci !== crewTab ? "" : `<fieldset class="crew">
    <legend><input class="crewName" data-crew="${ci}" data-f="crewName" value="${esc(c.name)}" aria-label="Crew name"></legend>
    ${(c.games || []).length ? `<p class="muted small">In the infopack for: ${esc(c.games.join("; "))}</p>` : ""}
    <div class="scroll"><table class="crewTable"><thead><tr><th>Position</th><th>Name</th><th>Pronouns</th><th>League affiliation</th><th>Certification</th><th>Head</th><th></th></tr></thead>
    <tbody>${(c.officials || []).map((o, oi) => `<tr class="${o.league && o.cert ? "" : "incomplete"}">
      <td><select data-crew="${ci}" data-off="${oi}" data-f="role" aria-label="Position">${roles.map((r) => `<option ${r === o.role ? "selected" : ""}>${esc(r)}</option>`).join("")}</select></td>
      <td>${field(ci, oi, "name", o.name)}</td><td>${field(ci, oi, "pronouns", o.pronouns, 8)}</td>
      <td>${field(ci, oi, "league", o.league)}</td><td>${field(ci, oi, "cert", o.cert)}</td>
      <td class="c"><input type="checkbox" data-crew="${ci}" data-off="${oi}" data-f="head" ${o.head ? "checked" : ""} aria-label="Head"></td>
      <td><button type="button" class="quiet" data-delofficial="${ci}:${oi}" title="remove">✕</button></td></tr>`).join("")}</tbody></table></div>
    <p><button type="button" data-addofficial="${ci}">Add official</button>
      <button type="button" class="quiet" data-delcrew="${ci}">Delete this crew</button>
      <span class="muted small">${(c.officials || []).filter((o) => !(o.league && o.cert)).length} without league or certification</span></p>
  </fieldset>`).join("");
}

function changed() { dirty = true; $("saveCrews").disabled = false; $("saveCrews").textContent = "Save crews"; }

$("crews").addEventListener("input", (e) => {
  const el = e.target, ci = el.dataset.crew;
  if (ci === undefined) return;
  const c = C[+ci];
  if (el.dataset.f === "crewName") {
    c.name = el.value;
    const tabLabel = document.querySelector(`#crewTabs [data-crewtab="${ci}"] .label`);
    if (tabLabel) tabLabel.textContent = c.name || "(no name)";
  } else {
    const o = c.officials[+el.dataset.off];
    o[el.dataset.f] = el.type === "checkbox" ? el.checked : el.value;
  }
  changed();
});
$("crews").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  if (b.dataset.addofficial !== undefined) { C[+b.dataset.addofficial].officials.push({ name: "", role: roles[0], league: "", cert: "" }); changed(); return renderCrews(); }
  if (b.dataset.delofficial) { const [ci, oi] = b.dataset.delofficial.split(":").map(Number); C[ci].officials.splice(oi, 1); changed(); return renderCrews(); }
  if (b.dataset.delcrew !== undefined) {
    if (!confirm(`Delete ${C[+b.dataset.delcrew].name}?`)) return;
    C.splice(+b.dataset.delcrew, 1); changed(); return renderCrews();
  }
});
$("newCrew").addEventListener("click", () => {
  C.push({ id: "", name: `Crew ${C.length + 1}`, officials: roles.slice(0, 1).map((r) => ({ name: "", role: r, head: true })) });
  crewTab = C.length - 1;
  changed();
  renderCrews();
});

async function crewRequest(url, opts, what) {
  $("crewMsg").textContent = `${what}…`;
  try {
    const r = await fetch(url, opts);
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json();
    C = d.crews || []; dirty = false; renderCrews();
    const rep = d.report;
    $("crewMsg").textContent = rep ? `${what}: found ${rep.matched} of ${rep.officials} officials, ${rep.changed} changed.` +
      (rep.notFound.length ? ` Not found: ${rep.notFound.join(", ")}.` : "") +
      ((rep.twice || []).length ? ` More than one official with the name ${rep.twice.join(", ")} on the roster: fill those in by hand.` : "") : `${what}: done.`;
    const ev = await (await fetch(`/t/${id}/data.json`)).json();
    E.crews = ev.crews || [];
    render();
    return true;
  } catch (e) {
    $("crewMsg").textContent = `${what}: ${e.message}`;
    return false;
  }
}

function formWith(file) {
  const f = new FormData();
  if (file) f.append("file", file);
  if ($("overwrite").checked) f.append("overwrite", "1");
  return f;
}

$("saveCrews").addEventListener("click", () => crewRequest(`/t/${id}/crews`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(C) }, "Saving the crews"));
$("fillRoster").addEventListener("click", async () => {
  if (dirty && !confirm("Save your changes first? Filling in from the roster starts from the saved crews.")) return;
  if (dirty) await crewRequest(`/t/${id}/crews`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(C) }, "Saving the crews");
  const ok = await crewRequest(`/t/${id}/crews/roster`, { method: "POST", body: formWith() }, "Filling in from WFTDA's roster");
  $("rosterFallback").hidden = ok;
});
$("fillRosterFile").addEventListener("change", (e) => {
  const f = e.target.files[0];
  if (f) crewRequest(`/t/${id}/crews/roster`, { method: "POST", body: formWith(f) }, "Filling in from the saved roster page").then((ok) => { $("rosterFallback").hidden = ok; });
  e.target.value = "";
});
$("fillList").addEventListener("change", (e) => {
  const f = e.target.files[0];
  if (f) crewRequest(`/t/${id}/crews/list`, { method: "POST", body: formWith(f) }, `Filling in from ${f.name}`);
  e.target.value = "";
});
window.addEventListener("beforeunload", (e) => { if (dirty) e.preventDefault(); });

$("allForm").addEventListener("submit", () => {
  $("allForm").action = `/t/${id}/all.zip`;
  $("allRules").value = opts.rules;
  $("allPaper").value = opts.paper;
});

// A download that fails (no charter, say) answers with text: show it instead
// of a broken file.
document.addEventListener("click", async (e) => {
  const a = e.target.closest("a.button");
  if (!a || !a.href.includes("/game/")) return;
  e.preventDefault();
  if (a.getAttribute("aria-disabled") === "true") return;
  const r = await fetch(a.href);
  if (!r.ok) { alert(await r.text()); return; }
  const name = (r.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/);
  const url = URL.createObjectURL(await r.blob());
  const link = Object.assign(document.createElement("a"), { href: url, download: name ? name[1] : "download" });
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
});

// While the page is in view it asks the server every 30 seconds, and when
// it comes back into view; crews only while nobody is editing them here.
async function poll() {
  if (document.hidden || !E) return;
  try {
    await refresh(true);
    if (!dirty && !busy($("crews"))) {
      const d = await (await fetch(`/t/${id}/crews`, { cache: "no-store" })).json();
      if (!dirty && JSON.stringify(d.crews || []) !== JSON.stringify(C)) { C = d.crews || []; renderCrews(); }
    }
  } catch (e) {} // offline for a moment: the next poll tries again
}
document.addEventListener("visibilitychange", poll);

load();
readTab();
fetch(`/t/${id}/data.json`).then((r) => r.json()).then((d) => {
  // Lists the server left out are empty lists.
  for (const k of ["teams", "games", "crews", "notes", "rulesets", "blanks"]) d[k] = d[k] || [];
  d.missing = d.missing || {};
  E = d;
  renderInfo();
  render();
  loadCrews();
  moveOldChoices();
  setInterval(poll, 30000);
}).catch((e) => {
  report(e.message, "data.json", 0, 0, e.stack);
  $("games").innerHTML = `<p class="error">Something went wrong showing this tournament: ${esc(e.message)}. It's in the log.</p>`;
});
