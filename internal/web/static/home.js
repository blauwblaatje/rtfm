"use strict";
// Errors in this page go to the server's log (logs/rtfm.log).
window.addEventListener("error", (e) => {
  try {
    navigator.sendBeacon("/log", new Blob([JSON.stringify({ message: e.message, source: e.filename || "", line: e.lineno || 0,
      column: e.colno || 0, stack: String(e.error && e.error.stack || ""), page: location.pathname, agent: navigator.userAgent })],
      { type: "application/json" }));
  } catch (x) {}
});
// The form takes a few seconds (every charter is downloaded): say so.
document.getElementById("loadForm").addEventListener("submit", () => {
  document.getElementById("go").disabled = true;
  document.getElementById("wait").hidden = false;
});
