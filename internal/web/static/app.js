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
// Choices are kept in this browser (localStorage), per tournament.
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const id = document.body.dataset.event;
const storeKey = "rtfm." + id;
let E = null; // the event (EventView)
let choices = {}; // game number -> {teams, colors, crew}
let opts = { rules: "", paper: "" };

function load() {
  try { const s = JSON.parse(localStorage.getItem(storeKey) || "{}"); choices = s.choices || {}; opts = { ...opts, ...(s.opts || {}) }; } catch (e) {}
}
function save() {
  try { localStorage.setItem(storeKey, JSON.stringify({ choices, opts })); } catch (e) {}
}

const team = (no) => E.teams.find((t) => t.no === no);
const choice = (g) => choices[g.no] || structuredClone(g.default);

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
  return `<input class="color" data-game="${g.no}" data-side="${side}" data-k="color" value="${esc(ch.colors[side])}" list="${list}"
      placeholder="colour" aria-label="Game ${g.no} ${side ? "away" : "home"} uniform colour">
    <datalist id="${list}">${(t && t.colors || []).map((c) => `<option value="${esc(c)}">`).join("")}</datalist>`;
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

function render() {
  const tracks = new Set(E.games.map((g) => g.track)).size;
  const days = {};
  for (const g of E.games) (days[g.date || "Date not known"] ||= []).push(g);
  $("games").innerHTML = Object.entries(days).map(([day, gs]) => `<h2>${esc(dayName(day))}</h2>
    <div class="scroll"><table class="games"><thead><tr><th>Game</th><th>Time</th><th>Home</th><th></th><th>Away</th><th>Crew</th><th>Download</th></tr></thead>
    <tbody>${gs.map((g) => {
      const ch = choice(g);
      const ready = ch.teams[0] && ch.teams[1] && ch.teams[0] !== ch.teams[1];
      const q = query(g, ch);
      return `<tr class="${ready ? "" : "waiting"}">
        <td class="no">${g.no}${tracks > 1 && g.track ? `<div class="muted small">track ${esc(g.track)}</div>` : ""}</td>
        <td>${esc(g.time)}</td>
        <td>${teamSelect(g, 0, ch)}<div>${colorInput(g, 0, ch)}</div></td>
        <td class="vs">vs</td>
        <td>${teamSelect(g, 1, ch)}<div>${colorInput(g, 1, ch)}</div></td>
        <td>${crewSelect(g, ch)}</td>
        <td class="dl">${ready ? `<a class="button" href="/t/${id}/game/${g.no}/statsbook.xlsx?${q}" ${E.blanks.length ? "" : 'aria-disabled="true" title="no blank statsbook on this site"'}>Statsbook</a>
          <a class="button" href="/t/${id}/game/${g.no}/java.json?${q}">CRG file</a>` : `<span class="muted small">${ch.teams[0] && ch.teams[0] === ch.teams[1] ? "same team twice" : "pick the teams"}</span>`}</td>
      </tr>${g.notes ? `<tr class="note"><td></td><td colspan="6" class="muted small">${esc(g.notes)}</td></tr>` : ""}`;
    }).join("")}</tbody></table></div>`).join("");
}

function dayName(d) {
  const t = new Date(d + "T12:00:00");
  return isNaN(t) ? d : t.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
}

function renderInfo() {
  $("meta").textContent = [E.dates, E.venue, E.host && `hosted by ${E.host}`].filter(Boolean).join(" · ");
  $("rules").innerHTML = E.rulesets.map((r) => `<option ${r === opts.rules ? "selected" : ""}>${esc(r)}</option>`).join("");
  if (!opts.rules) opts.rules = E.rulesets[0];
  $("paper").innerHTML = E.blanks.length ? E.blanks.map((b) => `<option ${b === opts.paper ? "selected" : ""}>${esc(b)}</option>`).join("")
    : `<option value="">none on this site</option>`;
  if (!E.blanks.includes(opts.paper)) opts.paper = E.blanks[0] || "";
  $("teams").innerHTML = `<table class="list"><thead><tr><th>#</th><th>Team</th><th>Charter</th><th>Skaters</th><th>Colours</th></tr></thead><tbody>
    ${E.teams.map((t) => `<tr><td>${t.no}</td><td>${esc(t.name)}</td><td>${t.missing ? `<span class="error">${esc(t.missing)}</span>` : esc(t.charter)}</td>
      <td>${t.skaters || ""}</td><td>${esc((t.colors || []).join(", "))}</td></tr>`).join("")}</tbody></table>`;
  $("notesBox").hidden = !E.notes.length;
  $("notes").innerHTML = E.notes.map((n) => `<li>${esc(n)}</li>`).join("");
  const missing = Object.keys(E.missing || {}).length;
  if (missing) $("teamsBox").open = true;
}

document.addEventListener("change", (e) => {
  const el = e.target;
  if (el.id === "rules" || el.id === "paper") { opts[el.id] = el.value; save(); return render(); }
  const no = el.dataset.game;
  if (!no) return;
  const g = E.games.find((x) => String(x.no) === no);
  const ch = choice(g);
  const side = +el.dataset.side;
  if (el.dataset.k === "team") {
    ch.teams[side] = +el.value;
    const t = team(ch.teams[side]);
    const cs = t && t.colors || [];
    ch.colors[side] = cs.length ? (side ? cs[cs.length - 1] : cs[0]) : "";
  }
  if (el.dataset.k === "color") ch.colors[side] = el.value.trim();
  if (el.dataset.k === "crew") ch.crew = el.value;
  choices[no] = ch;
  save();
  render();
});

$("reset").addEventListener("click", () => { choices = {}; save(); render(); });

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

function renderCrews() {
  $("saveCrews").disabled = !dirty;
  $("saveCrews").textContent = dirty ? "Save crews" : "Saved";
  if (!C.length) { $("crews").innerHTML = `<p class="muted">No crews yet: load an infopack with the tournament, or make one with New crew.</p>`; return; }
  const field = (ci, oi, k, v, size) => `<input data-crew="${ci}" data-off="${oi}" data-f="${k}" value="${esc(v)}" ${size ? `size="${size}"` : ""} aria-label="${k}">`;
  $("crews").innerHTML = C.map((c, ci) => `<fieldset class="crew">
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
  if (el.dataset.f === "crewName") c.name = el.value;
  else {
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
  const all = {};
  for (const g of E.games) all[g.no] = choice(g);
  $("allForm").action = `/t/${id}/all.zip`;
  $("allChoices").value = JSON.stringify(all);
  $("allRules").value = opts.rules;
  $("allPaper").value = opts.paper;
});

// A download that fails (no charter, say) answers with text: show it instead
// of a broken file.
document.addEventListener("click", async (e) => {
  const a = e.target.closest("a.button");
  if (!a) return;
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

load();
fetch(`/t/${id}/data.json`).then((r) => r.json()).then((d) => {
  // Lists the server left out are empty lists.
  for (const k of ["teams", "games", "crews", "notes", "rulesets", "blanks"]) d[k] = d[k] || [];
  d.missing = d.missing || {};
  E = d;
  renderInfo();
  render();
  loadCrews();
}).catch((e) => {
  report(e.message, "data.json", 0, 0, e.stack);
  $("games").innerHTML = `<p class="error">Something went wrong showing this tournament: ${esc(e.message)}. It's in the log.</p>`;
});
