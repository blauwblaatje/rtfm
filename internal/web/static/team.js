"use strict";
// A team's own page: its games, and per game the skaters who play, the
// captain, and (home team) bench and uniform colour. Kept on the server.
const token = document.body.dataset.token;
function report(message, source, line, column, stack) {
  try {
    const body = JSON.stringify({ message: String(message), source: source || "", line: line || 0, column: column || 0,
      stack: String(stack || ""), agent: navigator.userAgent });
    navigator.sendBeacon(`/team/${token}/log`, new Blob([body], { type: "application/json" }));
  } catch (e) {}
}
window.addEventListener("error", (e) => report(e.message, e.filename, e.lineno, e.colno, e.error && e.error.stack));
window.addEventListener("unhandledrejection", (e) => report(e.reason && e.reason.message || e.reason, "", 0, 0, e.reason && e.reason.stack));

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
let T = null; // TeamPageView
const forms = {}; // game number -> {skaters: Set, captain, bench, color, dirty, msg}

function dayName(d) {
  const t = new Date(d + "T12:00:00");
  return isNaN(t) ? d : t.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" });
}
const skaterName = (n) => { const s = T.skaters.find((x) => x.number === n); return s ? `${s.name} #${n}` : `#${n}`; };

function formFor(g) {
  if (!forms[g.no]) {
    const l = g.lineup || {};
    forms[g.no] = { skaters: new Set(l.skaters || []), captain: l.captain || "", bench: l.bench || "", color: l.color || "", dirty: false, msg: "" };
  }
  return forms[g.no];
}

function render() {
  $("meta").textContent = [T.tournament, T.dates, T.venue].filter(Boolean).join(" · ");
  if (T.missing || !T.skaters.length) {
    $("games").innerHTML = `<p class="error">There's no charter for your team here${T.missing ? `: ${esc(T.missing)}` : ""}. Ask the organisers.</p>`;
    return;
  }
  if (!T.games.length) { $("games").innerHTML = `<p class="muted">No games for your team (yet).</p>`; return; }
  $("games").innerHTML = T.games.map((g, gi) => {
    const f = formFor(g);
    const full = f.skaters.size >= T.max;
    const benchNote = g.home ? "" : g.bench ? `You're on the <b>${esc(g.bench)}</b> bench (${esc(g.opponent)}, the home team, picked the other).` :
      `${esc(g.opponent)}, the home team, hasn't picked its bench yet.`;
    const shut = g.open ? "" : "disabled";
    const when = g.open ? (g.closes ? `You can change this until ${esc(g.closes)}.` : "") :
      `<b>Closed${g.closes ? ` since ${esc(g.closes)}` : ""}</b>: ask the organisers if something has to change.`;
    const prev = T.games.slice(0, gi).reverse().find((p) => formFor(p).skaters.size);
    return `<fieldset class="teamGame" data-game="${g.no}">
      <legend>Game ${g.no} · ${esc(dayName(g.date))} ${esc(g.time)}${g.track ? ` · track ${esc(g.track)}` : ""}</legend>
      <p class="small ${g.open ? "muted" : ""}">${when}</p>
      <p><b>${g.home ? "Home" : "Away"}</b> against <b>${esc(g.opponent)}</b>.
        You wear ${g.home && g.open ? "the colour you pick below" : `<b>${esc(g.color || "not known yet")}</b>`};
        ${esc(g.opponent)} wears <b>${esc(g.opponentColor || "not known yet")}</b>.</p>
      ${g.home && g.open && g.lineup && g.lineup.color && g.color !== g.lineup.color ? `<p class="small"><b>The organisers made your colour ${esc(g.color)}</b>: ask them if that has to change.</p>` : ""}
      ${g.home ? `<div class="pickRow">
        <label>Bench <select data-k="bench" ${shut}><option value="">pick one</option>
          ${T.benches.map((b) => `<option ${b === f.bench ? "selected" : ""}>${esc(b)}</option>`).join("")}</select></label>
        <label>Uniform colour <input data-k="color" ${shut} value="${esc(f.color)}" list="colors${g.no}" placeholder="${esc(g.color || "colour")}">
          <datalist id="colors${g.no}">${T.colors.map((c) => `<option value="${esc(c)}">`).join("")}</datalist></label>
      </div>` : `<p class="muted small">${benchNote}</p>`}
      <p class="small"><b class="${f.skaters.size > T.max ? "error" : ""}">${f.skaters.size} of ${T.max}</b> skaters ticked
        ${g.open && prev ? `<button type="button" class="quiet small" data-copy="${prev.no}">Same as game ${prev.no}</button>` : ""}
        ${g.open ? `<button type="button" class="quiet small" data-clear>Untick all</button>` : ""}</p>
      <div class="skaters">${T.skaters.map((s) => {
        const on = f.skaters.has(s.number);
        return `<label class="check skater${on ? " on" : ""}"><input type="checkbox" data-skater="${esc(s.number)}" ${on ? "checked" : ""} ${!g.open || (!on && full) ? "disabled" : ""}>
          <b>#${esc(s.number)}</b> ${esc(s.name)}${s.pronouns ? ` <span class="muted">(${esc(s.pronouns)})</span>` : ""}</label>`;
      }).join("")}</div>
      <div class="pickRow">
        <label>Captain <select data-k="captain" ${shut}><option value="">pick one of the skaters playing</option>
          ${[...f.skaters].map((n) => `<option value="${esc(n)}" ${n === f.captain ? "selected" : ""}>${esc(skaterName(n))}</option>`).join("")}</select></label>
        <button type="button" class="primary" data-save ${f.dirty && g.open ? "" : "disabled"}>${f.dirty ? "Save" : g.lineup ? "Saved" : "Nothing to save"}</button>
      </div>
      <p class="small" role="status">${esc(f.msg || (g.lineup && !f.dirty ? `Saved ${new Date(g.lineup.updated).toLocaleString()}` : ""))}</p>
    </fieldset>`;
  }).join("");
}

function game(el) {
  const fs = el.closest("fieldset[data-game]");
  return fs && T.games.find((g) => String(g.no) === fs.dataset.game);
}

$("games").addEventListener("change", (e) => {
  const g = game(e.target);
  if (!g) return;
  const f = formFor(g), el = e.target;
  if (el.dataset.skater !== undefined) {
    if (el.checked) f.skaters.add(el.dataset.skater);
    else { f.skaters.delete(el.dataset.skater); if (f.captain === el.dataset.skater) f.captain = ""; }
  } else if (el.dataset.k) f[el.dataset.k] = el.value.trim();
  f.dirty = true; f.msg = "";
  render();
});
$("games").addEventListener("click", async (e) => {
  const b = e.target.closest("button");
  const g = b && game(b);
  if (!g) return;
  const f = formFor(g);
  if (b.dataset.copy) {
    const from = forms[b.dataset.copy];
    f.skaters = new Set(from.skaters); f.captain = from.captain; f.dirty = true; f.msg = "";
    return render();
  }
  if (b.dataset.clear !== undefined) { f.skaters = new Set(); f.captain = ""; f.dirty = true; f.msg = ""; return render(); }
  if (b.dataset.save !== undefined) {
    b.disabled = true;
    try {
      const body = { skaters: T.skaters.map((s) => s.number).filter((n) => f.skaters.has(n)), captain: f.captain, bench: f.bench, color: f.color };
      const r = await fetch(`/team/${token}/game/${g.no}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!r.ok) throw new Error(await r.text());
      g.lineup = await r.json();
      if (g.home && g.lineup.color) g.color = g.lineup.color;
      f.dirty = false;
      f.msg = `Saved. ${f.skaters.size < T.max ? `${f.skaters.size} skaters: you can add up to ${T.max}. ` : ""}${f.captain ? "" : "No captain picked yet. "}` +
        (g.home && !f.bench ? "No bench picked yet." : "");
    } catch (err) {
      f.msg = `Not saved: ${err.message}`;
    }
    render();
  }
});
window.addEventListener("beforeunload", (e) => { if (Object.values(forms).some((f) => f.dirty)) e.preventDefault(); });

// The page asks the server every 30 seconds while it's in view, and when
// it comes back into view: the other team's bench, colours, the deadline
// and what's saved from elsewhere show up by themselves. A game with
// changes not saved yet keeps them; nothing is redrawn while typing or
// picking.
let lastData = "";
async function load(polled) {
  const r = await fetch(`/team/${token}/data.json`, { cache: "no-store" });
  if (!r.ok) throw new Error(`the page's data: ${r.status}`);
  const text = await r.text();
  const a = document.activeElement;
  if (polled && (text === lastData || (a && $("games").contains(a) && (a.tagName === "SELECT" || (a.tagName === "INPUT" && a.type !== "checkbox"))))) return;
  lastData = text;
  T = JSON.parse(text);
  for (const no in forms) if (!forms[no].dirty) delete forms[no];
  render();
}
async function poll() {
  if (document.hidden || !T) return;
  try { await load(true); } catch (e) {} // offline for a moment: the next poll tries again
}
document.addEventListener("visibilitychange", poll);

load().then(() => setInterval(poll, 30000)).catch((e) => {
  report(e.message, "data.json", 0, 0, e.stack);
  $("games").innerHTML = `<p class="error">Something went wrong showing your games: ${esc(e.message)}. It's in the organisers' log.</p>`;
});
