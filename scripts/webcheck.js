// Provjera sučelja prije isporuke. Hvata dvije greške koje se u pregledniku
// ne vide odmah, a modul tiho ne radi:
//
//   1. dva elementa s istim id-em — getElementById vrati prvi, pa se gumb
//      jednog modula zapravo veže na tuđi (nađeno: offsite backup i OSPF su
//      dijelili os-save, pa "Spremi" kod backupa nije bio njegov)
//   2. $("nesto") u JS-u za element koji u HTML-u ne postoji — modul pukne
//      pri prvom otvaranju
//
// Pokreće se iz scripts/build.sh; izlazi s greškom da build stane.
const fs = require("fs");
const path = require("path");

const root = path.join(__dirname, "..");
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const js = fs.readFileSync(path.join(root, "web/app.js"), "utf8");

let fail = 0;

const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const seen = new Set();
const dup = new Set();
for (const id of ids) {
  if (seen.has(id)) dup.add(id);
  seen.add(id);
}
if (dup.size) {
  console.error("GREŠKA: dvostruki id u index.html: " + [...dup].join(", "));
  fail = 1;
}

// element se doseže na tri načina; setNote prima goli id i tiho ne radi ništa
// ako ga nema, pa se takva greška u pregledniku ne vidi (nađeno 05.08.2026.)
const used = [...new Set([
  ...[...js.matchAll(/\$\("([^"]+)"\)/g)].map((m) => m[1]),
  ...[...js.matchAll(/setNote\("([^"]+)"/g)].map((m) => m[1]),
  ...[...js.matchAll(/getElementById\("([^"]+)"\)/g)].map((m) => m[1]),
])];
const missing = used.filter((id) => !seen.has(id));
if (missing.length) {
  console.error("GREŠKA: app.js traži elemente kojih nema: " + missing.join(", "));
  fail = 1;
}

// svaki modul iz izbornika mora imati svoj ekran
const modules = [...js.matchAll(/^ {2}(\w+):\s+\[/gm)].map((m) => m[1]);
const noView = modules.filter((m) => !seen.has("view-" + m));
if (noView.length) {
  console.error("GREŠKA: moduli bez ekrana: " + noView.join(", "));
  fail = 1;
}

if (!fail) {
  console.log(`sučelje: ${ids.length} elemenata, ${used.length} korištenih iz JS-a, ` +
    `${modules.length} modula — bez dvostrukih id-eva`);
}
process.exit(fail);
