"use strict";
// The form takes a few seconds (every charter is downloaded): say so.
document.getElementById("loadForm").addEventListener("submit", () => {
  document.getElementById("go").disabled = true;
  document.getElementById("wait").hidden = false;
});
