import { paintBlossoms } from "../brief/icons.js";
import { inExtension } from "../lib/api.js";

paintBlossoms();

// Served by the server the link is "/"; inside the extension it is the
// extension's own brief page.
if (inExtension()) {
  document.getElementById("back").href = chrome.runtime.getURL("src/brief/brief.html");
}
