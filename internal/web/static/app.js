"use strict";
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
    <datalist id="${list}">${(t ? t.colors : []).map((c) => `<option value="${esc(c)}">`).join("")}</datalist>`;
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
      <td>${t.skaters || ""}</td><td>${esc(t.colors.join(", "))}</td></tr>`).join("")}</tbody></table>`;
  $("crewsBox").hidden = !E.crews.length;
  $("crews").innerHTML = `<table class="list"><thead><tr><th>Crew</th><th>Officials</th><th>Heads</th><th>Games in the infopack</th></tr></thead><tbody>
    ${E.crews.map((c) => `<tr><td>${esc(c.name)}</td><td>${c.officials}</td><td>${esc(c.heads.join(", "))}</td><td>${esc((c.games || []).join("; "))}</td></tr>`).join("")}</tbody></table>`;
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
    ch.colors[side] = t && t.colors.length ? (side ? t.colors[t.colors.length - 1] : t.colors[0]) : "";
  }
  if (el.dataset.k === "color") ch.colors[side] = el.value.trim();
  if (el.dataset.k === "crew") ch.crew = el.value;
  choices[no] = ch;
  save();
  render();
});

$("reset").addEventListener("click", () => { choices = {}; save(); render(); });

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
fetch(`/t/${id}/data.json`).then((r) => r.json()).then((d) => { E = d; renderInfo(); render(); })
  .catch((e) => { $("games").innerHTML = `<p class="error">${esc(e.message)}</p>`; });
