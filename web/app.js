/* Saguaro Dashboard v1 — čita isključivo saguaro-core API v1 */
"use strict";

const $ = (id) => document.getElementById(id);
const API = "/api/v1";
let token = localStorage.getItem("saguaro_token") || "";
let cores = 1;
let timers = [];

/* ---------- pomoćne ---------- */

async function api(path, method = "GET", body = null) {
  const opts = {
    method,
    headers: token ? { Authorization: "Bearer " + token } : {},
  };
  if (body !== null) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(API + path, opts);
  if (r.status === 401) throw { unauthorized: true };
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || path + ": HTTP " + r.status);
  return data;
}

const GB = 1024 * 1024 * 1024;
function fmtBytes(b) {
  if (b >= GB) return (b / GB).toFixed(1) + " GB";
  return (b / (1024 * 1024)).toFixed(0) + " MB";
}
function fmtKB(kb) { return fmtBytes(kb * 1024); }

function fmtUptime(s) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600),
        m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  return `${m} min`;
}

function setMeter(el, pct) {
  el.style.width = Math.min(100, pct).toFixed(1) + "%";
  el.classList.toggle("crit", pct >= 95);
  el.classList.toggle("warn", pct >= 80 && pct < 95);
}

function st(cls, icon, text) {
  const s = document.createElement("span");
  s.className = "st " + cls;
  s.textContent = icon + " " + text;
  return s;
}
const stGood = (t) => st("st-good", "✓", t);
const stWarn = (t) => st("st-warn", "△", t);
const stCrit = (t) => st("st-crit", "✕", t);
const stOff  = (t) => st("st-off", "○", t);

/* ---------- render ---------- */

function renderSystem(sys) {
  cores = sys.cpu_cores || 1;
  $("hostname").textContent = sys.hostname;
  const kv = $("system-kv");
  kv.replaceChildren();
  const rows = [
    ["Model", sys.model],
    ["Firmware", sys.firmware],
    ["Kernel", sys.kernel],
    ["Target", sys.target + " · rootfs " + sys.rootfs],
    ["Saguaro Core", "v" + sys.saguaro_version],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
  $("versions").textContent =
    `${sys.firmware} · Saguaro Core v${sys.saguaro_version}`;
}

function renderStatus(x) {
  const load1 = x.load[0];
  $("t-cpu").textContent = load1.toFixed(2);
  $("t-cpu-sub").textContent = `1 min prosjek · ${cores} jezgre`;
  setMeter($("m-cpu"), (load1 / cores) * 100);

  const m = x.memory;
  const used = m.total - m.available;
  const pct = (used / m.total) * 100;
  $("t-ram").textContent = pct.toFixed(0) + " %";
  $("t-ram-sub").textContent = `${fmtBytes(used)} od ${fmtBytes(m.total)}`;
  setMeter($("m-ram"), pct);

  $("t-uptime").textContent = fmtUptime(x.uptime_seconds);
}

function renderStorage(x) {
  const root = x.filesystems.find((f) => f.mount === "/");
  if (!root) return;
  $("t-disk").textContent = root.used_percent.toFixed(1) + " %";
  $("t-disk-sub").textContent =
    `${fmtKB(root.used_kb)} od ${fmtKB(root.total_kb)}`;
  setMeter($("m-disk"), root.used_percent);
}

function renderHealth(h) {
  const badge = $("health-badge");
  const b = h.status === "ok" ? stGood("Sustav u redu") : stWarn("Degradirano");
  b.id = "health-badge";
  badge.replaceWith(b);

  const list = $("health-checks");
  list.replaceChildren();
  const items = [
    ["Gateway", h.gateway.address || "nepoznat", h.gateway.reachable],
    ["DNS razrješavanje", "", h.dns.ok],
    ["Internet", "1.1.1.1:443", h.internet.ok],
  ];
  for (const [name, detail, ok] of items) {
    const li = document.createElement("li");
    const left = document.createElement("span");
    left.className = "what";
    left.textContent = name;
    if (detail) {
      const d = document.createElement("span");
      d.className = "detail";
      d.textContent = detail;
      left.append(d);
    }
    li.append(left, ok ? stGood("Radi") : stCrit("Ne radi"));
    list.append(li);
  }
}

function renderInterfaces(x) {
  // portovi: samo fizički ethX
  const ports = $("ports");
  ports.replaceChildren();
  for (const d of x.devices.filter((d) => d.name.startsWith("eth"))) {
    const div = document.createElement("div");
    div.className = "port" + (d.carrier ? " link" : "");
    const name = document.createElement("div");
    name.className = "port-name";
    name.textContent = d.name;
    const state = d.carrier
      ? stGood("Link " + (d.speed ? d.speed.replace("F", "") + " Mbit" : ""))
      : stOff("Nema linka");
    const mac = document.createElement("div");
    mac.className = "port-mac";
    mac.textContent = d.mac;
    div.append(name, state, mac);
    ports.append(div);
  }

  // logička sučelja
  const tb = $("iface-rows");
  tb.replaceChildren();
  for (const i of x.interfaces) {
    const tr = document.createElement("tr");
    const cells = [
      i.name,
      null, // stanje
      i.proto,
      i.device || "—",
      i.ipv4.length ? i.ipv4.join(", ") : "—",
      i.gateway || "—",
      i.dns && i.dns.length ? i.dns.join(", ") : "—",
      i.up ? fmtUptime(i.uptime_seconds) : "—",
    ];
    cells.forEach((c, n) => {
      const td = document.createElement("td");
      if (n === 1) td.append(i.up ? stGood("Aktivno") : stOff("Neaktivno"));
      else td.textContent = c;
      if (n === 7) td.className = "num";
      tr.append(td);
    });
    tb.append(tr);
  }
}

/* ---------- inventory: uređaji ---------- */

let editUUID = null; // null = novi uređaj
let editIsSelf = false;

async function loadDevices() {
  const x = await api("/inventory/devices");
  const tb = $("dev-rows");
  tb.replaceChildren();
  for (const d of x.devices) {
    const tr = document.createElement("tr");

    const tdName = document.createElement("td");
    tdName.textContent = d.hostname;
    if (d.is_self) {
      const b = document.createElement("span");
      b.className = "badge";
      b.textContent = "ovaj uređaj";
      tdName.append(b);
    }
    tr.append(tdName);

    for (const v of [d.model, d.firmware, d.serial, d.location, d.customer, d.notes]) {
      const td = document.createElement("td");
      td.textContent = v || "—";
      tr.append(td);
    }

    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const edit = document.createElement("button");
    edit.className = "btn-sm";
    edit.textContent = "Uredi";
    edit.onclick = () => openDeviceDialog(d);
    tdAct.append(edit);
    if (!d.is_self) {
      const del = document.createElement("button");
      del.className = "btn-sm danger";
      del.textContent = "Obriši";
      del.onclick = async () => {
        if (!confirm(`Obrisati uređaj "${d.hostname}"?`)) return;
        await api("/inventory/devices/" + d.uuid, "DELETE").catch(alertErr);
        loadDevices().catch(onTickError);
      };
      tdAct.append(del);
    }
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openDeviceDialog(d) {
  const f = $("dev-form");
  editUUID = d ? d.uuid : null;
  editIsSelf = d ? d.is_self : false;
  $("dev-dialog-title").textContent = d ? "Uredi uređaj" : "Novi uređaj";
  $("dev-self-note").classList.toggle("hidden", !editIsSelf);
  for (const el of f.elements) {
    if (!el.name) continue;
    el.value = d ? d[el.name] || "" : "";
    // hardverska polja ovog uređaja puni samoregistracija
    el.disabled = editIsSelf && !["location", "customer", "notes"].includes(el.name);
  }
  $("dev-dialog").showModal();
}

function alertErr(e) {
  if (e && e.unauthorized) { logout(true); return; }
  alert("Greška: " + (e.message || e));
}

/* ---------- dhcp ---------- */

let editHostUUID = null;
let dhcpEnabled = true;

async function loadDhcp() {
  const [st, hs] = await Promise.all([api("/dhcp/status"), api("/inventory/hosts")]);

  const kv = $("dhcp-server-kv");
  kv.replaceChildren();
  const sv = st.server || {};
  const rows = [
    ["Sučelje", sv.interface || "—"],
    ["Početak raspona", sv.start || "—"],
    ["Veličina raspona", sv.limit || "—"],
    ["Trajanje leasea", sv.leasetime || "—"],
    ["Stanje", sv.ignore ? "isključen" : "aktivan"],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }

  dhcpEnabled = !sv.ignore;
  $("dhcp-toggle").textContent = dhcpEnabled
    ? "Isključi DHCP poslužitelj" : "Uključi DHCP poslužitelj";
  $("dhcp-srv-hint").textContent = dhcpEnabled
    ? "⚠ Aktivan DHCP poslužitelj na mreži s postojećim routerom može dijeliti " +
      "krive adrese klijentima (rogue DHCP). Isključi ga ako adrese dijeli router."
    : "DHCP poslužitelj je isključen — rezervacije se primjenjuju, ali ne " +
      "dijele dok ga ponovno ne uključiš.";

  const managedDB = hs.hosts.filter((h) => h.managed).length;
  const sagOnDev = st.static_leases.filter((l) => l.managed_by_saguaro).length;
  const foreign = st.static_leases.length - sagOnDev;
  let info = `U bazi upravljanih hostova: ${managedDB} · Saguaro rezervacija na uređaju: ${sagOnDev}`;
  if (foreign > 0) info += ` · ostalih (ručnih/LuCI, ne diraju se): ${foreign}`;
  if (managedDB !== sagOnDev) info += " — ⚠ razlika, potrebna primjena";
  $("dhcp-sync-info").textContent = info;

  // hosts iz inventoryja
  const tb = $("host-rows");
  tb.replaceChildren();
  const knownMacs = new Set();
  for (const h of hs.hosts) {
    knownMacs.add(h.mac);
    const tr = document.createElement("tr");
    for (const v of [h.hostname || "—", h.mac, h.ipv4 || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdM = document.createElement("td");
    tdM.append(h.managed ? stGood("Da") : stOff("Ne"));
    tr.append(tdM);
    for (const v of [h.customer || "—", h.notes || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const edit = document.createElement("button");
    edit.className = "btn-sm";
    edit.textContent = "Uredi";
    edit.onclick = () => openHostDialog(h);
    const del = document.createElement("button");
    del.className = "btn-sm danger";
    del.textContent = "Obriši";
    del.onclick = async () => {
      if (!confirm(`Obrisati host "${h.hostname || h.mac}"?`)) return;
      await api("/inventory/hosts/" + h.uuid, "DELETE").catch(alertErr);
      loadDhcp().catch(alertErr);
    };
    tdAct.append(edit, del);
    tr.append(tdAct);
    tb.append(tr);
  }

  // aktivni leaseovi
  const lb = $("lease-rows");
  lb.replaceChildren();
  for (const l of st.active_leases) {
    const tr = document.createElement("tr");
    for (const v of [l.hostname || "—", l.mac, l.ip,
      l.expires_at ? new Date(l.expires_at * 1000).toLocaleString("hr-HR") : "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    if (!knownMacs.has(l.mac)) {
      const add = document.createElement("button");
      add.className = "btn-sm";
      add.textContent = "U rezervacije";
      add.onclick = () => openHostDialog({
        hostname: l.hostname, mac: l.mac, ipv4: l.ip, managed: true,
      });
      tdAct.append(add);
    }
    tr.append(tdAct);
    lb.append(tr);
  }
}

function openHostDialog(h) {
  const f = $("host-form");
  editHostUUID = h && h.uuid ? h.uuid : null;
  $("host-dialog-title").textContent = editHostUUID ? "Uredi host" : "Novi host";
  for (const el of f.elements) {
    if (!el.name) continue;
    if (el.type === "checkbox") el.checked = h ? !!h[el.name] : false;
    else el.value = h ? h[el.name] || "" : "";
  }
  $("host-dialog").showModal();
}

/* ---------- dns ---------- */

let editRecUUID = null;
let dnsDomain = "lan";

async function loadDns() {
  const [st, rc] = await Promise.all([api("/dns/status"), api("/dns/records")]);

  const dm = st.dnsmasq || {};
  dnsDomain = dm.domain || "lan";
  const kv = $("dns-server-kv");
  kv.replaceChildren();
  const rows = [
    ["Lokalna domena", dm.domain || "—"],
    ["Lokalne zone", dm.local || "—"],
    ["Zaštita od DNS rebinda", dm.rebind_protection ? "uključena" : "isključena"],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }

  const enabledDB = rc.records.filter((r) => r.enabled).length;
  const sagOnDev = st.entries.filter((e) => e.managed_by_saguaro).length;
  const foreign = st.entries.length - sagOnDev;
  let info = `U bazi aktivnih zapisa: ${enabledDB} · Saguaro zapisa na uređaju: ${sagOnDev}`;
  if (foreign > 0) info += ` · ostalih (ručnih/LuCI, ne diraju se): ${foreign}`;
  if (enabledDB !== sagOnDev) info += " — ⚠ razlika, potrebna primjena";
  $("dns-sync-info").textContent = info;

  const tb = $("rec-rows");
  tb.replaceChildren();
  for (const rec of rc.records) {
    const tr = document.createElement("tr");
    const shown = rec.name.includes(".") ? rec.name : rec.name + "." + dnsDomain;
    for (const v of [shown, rec.type, rec.value]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(rec.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdN = document.createElement("td");
    tdN.textContent = rec.notes || "—";
    tr.append(tdN);

    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const edit = document.createElement("button");
    edit.className = "btn-sm";
    edit.textContent = "Uredi";
    edit.onclick = () => openRecDialog(rec);
    const del = document.createElement("button");
    del.className = "btn-sm danger";
    del.textContent = "Obriši";
    del.onclick = async () => {
      if (!confirm(`Obrisati DNS zapis "${rec.name}"?`)) return;
      await api("/dns/records/" + rec.uuid, "DELETE").catch(alertErr);
      loadDns().catch(alertErr);
    };
    tdAct.append(edit, del);
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openRecDialog(rec) {
  const f = $("rec-form");
  editRecUUID = rec ? rec.uuid : null;
  $("rec-dialog-title").textContent = editRecUUID ? "Uredi DNS zapis" : "Novi DNS zapis";
  f.elements.name.value = rec ? rec.name : "";
  f.elements.rtype.value = rec ? rec.type : "A";
  f.elements.value.value = rec ? rec.value : "";
  f.elements.notes.value = rec ? rec.notes || "" : "";
  f.elements.enabled.checked = rec ? !!rec.enabled : true;
  $("rec-dialog").showModal();
}

/* ---------- wireguard ---------- */

let editPeerUUID = null;

function fmtAgo(epoch) {
  if (!epoch) return "—";
  const s = Math.floor(Date.now() / 1000 - epoch);
  if (s < 0) return "—";
  if (s < 90) return "prije " + s + " s";
  if (s < 5400) return "prije " + Math.round(s / 60) + " min";
  return "prije " + Math.round(s / 3600) + " h";
}

async function loadWireguard() {
  const [st, ps] = await Promise.all([
    api("/wireguard/status"), api("/wireguard/peers"),
  ]);

  const srv = st.server || {};
  const f = $("wg-form");
  if (srv.configured) {
    f.elements.listen_port.value = srv.listen_port || "";
    f.elements.address.value = (srv.addresses || []).join(", ");
    f.elements.endpoint_host.value = srv.endpoint_host || "";
    f.elements.client_dns.value = srv.client_dns || "";
    f.elements.client_allowed_ips.value = srv.client_allowed_ips || "";
  }

  const kv = $("wg-kv");
  kv.replaceChildren();
  const rows = [
    ["Paketi", st.installed ? "instalirani" : "nedostaju (kmod-wireguard, wireguard-tools)"],
    ["Sučelje " + "sag_wg0", st.running ? "aktivno" : srv.configured ? "neaktivno" : "nije konfigurirano"],
    ["Javni ključ", srv.public_key || "—"],
    ["Port", srv.listen_port || "—"],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    dd.style.wordBreak = "break-all";
    kv.append(dt, dd);
  }

  const enabledDB = ps.peers.filter((p) => p.enabled).length;
  let info = `U bazi aktivnih peerova: ${enabledDB} · na uređaju: ${st.uci_peers}`;
  if (enabledDB !== st.uci_peers) info += " — ⚠ razlika, potrebna primjena";
  $("wg-sync-info").textContent = info;

  const tb = $("peer-rows");
  tb.replaceChildren();
  for (const p of ps.peers) {
    const stat = (st.stats || {})[p.public_key];
    const tr = document.createElement("tr");
    for (const v of [p.name, p.tunnel_ip, p.public_key.slice(0, 12) + "…"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(p.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdH = document.createElement("td");
    tdH.textContent = stat ? fmtAgo(stat.latest_handshake) : "—";
    tr.append(tdH);
    const tdT = document.createElement("td");
    tdT.textContent = stat && (stat.rx_bytes || stat.tx_bytes)
      ? fmtBytes(stat.rx_bytes) + " / " + fmtBytes(stat.tx_bytes) : "—";
    tr.append(tdT);

    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    if (p.has_private) {
      const conf = document.createElement("button");
      conf.className = "btn-sm";
      conf.textContent = "Config";
      conf.onclick = async () => {
        try {
          const c = await api("/wireguard/peers/" + p.uuid + "/config");
          $("wgconf-title").textContent = "Klijentski config — " + c.name;
          $("wgconf-text").value = c.config;
          $("wgconf-dialog").showModal();
        } catch (e) { alertErr(e); }
      };
      tdAct.append(conf);
    }
    const edit = document.createElement("button");
    edit.className = "btn-sm";
    edit.textContent = "Uredi";
    edit.onclick = () => openPeerDialog(p);
    const del = document.createElement("button");
    del.className = "btn-sm danger";
    del.textContent = "Obriši";
    del.onclick = async () => {
      if (!confirm(`Obrisati peer "${p.name}"? Njegov ključ se ne može vratiti.`)) return;
      await api("/wireguard/peers/" + p.uuid, "DELETE").catch(alertErr);
      loadWireguard().catch(alertErr);
    };
    tdAct.append(edit, del);
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openPeerDialog(p) {
  const f = $("peer-form");
  editPeerUUID = p ? p.uuid : null;
  $("peer-dialog-title").textContent = editPeerUUID ? "Uredi peer" : "Novi peer";
  f.elements.name.value = p ? p.name : "";
  f.elements.tunnel_ip.value = p ? p.tunnel_ip : "";
  f.elements.public_key.value = p ? p.public_key : "";
  // ključ je identitet peera — kod uređivanja se ne mijenja
  f.elements.public_key.disabled = !!editPeerUUID;
  f.elements.keepalive.value = p && p.keepalive ? p.keepalive : "";
  f.elements.notes.value = p ? p.notes || "" : "";
  f.elements.enabled.checked = p ? !!p.enabled : true;
  $("peer-dialog").showModal();
}

/* ---------- postavke ---------- */

let tokVisible = false;

async function loadSettings() {
  const s = await api("/auth/session");
  const kv = $("sess-kv");
  kv.replaceChildren();
  for (const [k, v] of [["Prijavljen kao", s.username],
    ["Aktivnih sesija", s.active_sessions]]) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
  tokVisible = false;
  $("tok-value").textContent = "••••••••••••";
  $("tok-show").textContent = "Prikaži";
  $("tok-result").textContent = "";
  $("pw-result").textContent = "";
  $("sess-result").textContent = "";
}

/* ---------- backup ---------- */

async function apiBlob(path) {
  const r = await fetch(API + path, {
    headers: { Authorization: "Bearer " + token },
  });
  if (r.status === 401) throw { unauthorized: true };
  if (!r.ok) throw new Error(path + ": HTTP " + r.status);
  return r.blob();
}

async function downloadBackup(name) {
  const blob = await apiBlob("/backup/download/" + encodeURIComponent(name));
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function backupRow(b, actions) {
  const tr = document.createElement("tr");
  for (const v of [b.name, fmtBytes(b.size_bytes),
    new Date(b.modified_at * 1000).toLocaleString("hr-HR")]) {
    const td = document.createElement("td");
    td.textContent = v;
    tr.append(td);
  }
  const tdAct = document.createElement("td");
  tdAct.className = "row-actions";
  tdAct.append(...actions);
  tr.append(tdAct);
  return tr;
}

function btnSm(label, danger, onclick) {
  const b = document.createElement("button");
  b.className = "btn-sm" + (danger ? " danger" : "");
  b.textContent = label;
  b.onclick = onclick;
  return b;
}

async function loadBackup() {
  const x = await api("/backup/archives");

  const tb = $("bk-rows");
  tb.replaceChildren();
  for (const b of x.archives) {
    tb.append(backupRow(b, [
      btnSm("Preuzmi", false, () => downloadBackup(b.name).catch(alertErr)),
      btnSm("Vrati", true, () => restoreBackup(b.name)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati arhivu "${b.name}"?`)) return;
        await api("/backup/archives/" + encodeURIComponent(b.name), "DELETE")
          .catch(alertErr);
        loadBackup().catch(alertErr);
      }),
    ]));
  }

  const cb = $("cfg-rows");
  cb.replaceChildren();
  for (const b of x.config_backups) {
    cb.append(backupRow(b, [
      btnSm("Preuzmi", false, () => downloadBackup(b.name).catch(alertErr)),
    ]));
  }
}

async function restoreBackup(name) {
  if (!confirm(
    `Vratiti backup "${name}"?\n\n` +
    `Ovo PREPISUJE cijelu konfiguraciju uređaja (mrežu, DHCP, DNS, VPN, ` +
    `Saguaro bazu) i PONOVNO POKREĆE uređaj.`)) return;
  if (!confirm("Zadnja provjera: uređaj će se odmah rebootati. Nastaviti?")) return;
  try {
    await api("/backup/restore", "POST", { name });
    stopTimers();
    $("bk-create-result").textContent =
      "Backup vraćen — uređaj se ponovno pokreće. Pričekaj ~2 minute pa " +
      "osvježi stranicu (adresa uređaja može biti ona iz backupa).";
  } catch (e) {
    alertErr(e);
  }
}

/* ---------- mreža ---------- */

async function loadNetwork() {
  const x = await api("/network/lan");
  const f = $("net-form");
  for (const name of ["ipaddr", "netmask", "gateway", "dns"])
    f.elements[name].value = x[name] || "";
}

/* ---------- router ---------- */

function route() {
  const view = location.hash.startsWith("#/devices") ? "devices"
    : location.hash.startsWith("#/dhcp") ? "dhcp"
    : location.hash.startsWith("#/dns") ? "dns"
    : location.hash.startsWith("#/wireguard") ? "wireguard"
    : location.hash.startsWith("#/backup") ? "backup"
    : location.hash.startsWith("#/settings") ? "settings"
    : location.hash.startsWith("#/network") ? "network" : "dashboard";
  for (const v of ["dashboard", "devices", "dhcp", "dns", "wireguard", "backup",
    "settings", "network"]) {
    $("view-" + v).classList.toggle("hidden", v !== view);
    $("tab-" + v).classList.toggle("active", v === view);
  }
  if (!token) return;
  if (view === "devices") loadDevices().catch(alertErr);
  if (view === "dhcp") loadDhcp().catch(alertErr);
  if (view === "dns") loadDns().catch(alertErr);
  if (view === "wireguard") loadWireguard().catch(alertErr);
  if (view === "backup") loadBackup().catch(alertErr);
  if (view === "settings") loadSettings().catch(alertErr);
  if (view === "network") loadNetwork().catch(alertErr);
}
window.addEventListener("hashchange", route);

/* ---------- petlje ---------- */

async function tickFast() {
  const [status, storage, ifaces] = await Promise.all([
    api("/system/status"), api("/storage"), api("/interfaces"),
  ]);
  renderStatus(status);
  renderStorage(storage);
  renderInterfaces(ifaces);
  $("refreshed").textContent =
    "osvježeno " + new Date().toLocaleTimeString("hr-HR");
}

async function tickSlow() {
  renderHealth(await api("/health"));
}

function stopTimers() { timers.forEach(clearInterval); timers = []; }

async function start() {
  try {
    renderSystem(await api("/system"));
    await Promise.all([tickFast(), tickSlow()]);
  } catch (e) {
    if (e && e.unauthorized) { logout(true); return; }
    throw e;
  }
  $("login").classList.add("hidden");
  $("app").classList.remove("hidden");
  timers.push(setInterval(() => tickFast().catch(onTickError), 5000));
  timers.push(setInterval(() => tickSlow().catch(onTickError), 15000));
  route();
}

function onTickError(e) {
  if (e && e.unauthorized) logout(true);
  else $("refreshed").textContent = "uređaj nedostupan — pokušavam ponovno";
}

function logout(showError) {
  // pri ručnoj odjavi poništi sesiju i na uređaju (best effort);
  // kod 401 odjave sesija je ionako nevaljana
  if (token && !showError) api("/auth/logout", "POST", {}).catch(() => {});
  stopTimers();
  localStorage.removeItem("saguaro_token");
  token = "";
  $("app").classList.add("hidden");
  $("login").classList.remove("hidden");
  $("login-error").classList.toggle("hidden", !showError);
}

/* ---------- init ---------- */

$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  $("login-error").classList.add("hidden");
  try {
    const r = await fetch(API + "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username: $("user-input").value.trim(),
        password: $("pass-input").value,
      }),
    });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(data.error || "HTTP " + r.status);
    token = data.token;
    localStorage.setItem("saguaro_token", token);
    $("pass-input").value = "";
    await start();
  } catch {
    logout(true);
  }
});
$("logout").addEventListener("click", () => logout(false));

$("dev-add").addEventListener("click", () => openDeviceDialog(null));
$("dev-cancel").addEventListener("click", () => $("dev-dialog").close());
$("dev-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  const fields = editIsSelf
    ? ["location", "customer", "notes"]
    : ["hostname", "model", "firmware", "serial", "location", "customer", "notes"];
  for (const name of fields) body[name] = f.elements[name].value.trim();
  try {
    if (editUUID) await api("/inventory/devices/" + editUUID, "PUT", body);
    else await api("/inventory/devices", "POST", body);
    $("dev-dialog").close();
    await loadDevices();
  } catch (e) {
    alertErr(e);
  }
});

$("host-add").addEventListener("click", () => openHostDialog(null));
$("host-cancel").addEventListener("click", () => $("host-dialog").close());
$("host-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const name of ["hostname", "mac", "ipv4", "customer", "notes"])
    body[name] = f.elements[name].value.trim();
  body.managed = f.elements.managed.checked;
  try {
    if (editHostUUID) await api("/inventory/hosts/" + editHostUUID, "PUT", body);
    else await api("/inventory/hosts", "POST", body);
    $("host-dialog").close();
    await loadDhcp();
  } catch (e) {
    alertErr(e);
  }
});

$("dhcp-apply").addEventListener("click", async () => {
  const btn = $("dhcp-apply");
  btn.disabled = true;
  $("dhcp-apply-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/dhcp/apply", "POST", {});
    let msg = `Primijenjeno: ${r.applied} rezervacija (uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    if (r.skipped && r.skipped.length) msg += ` · preskočeno: ${r.skipped.join(", ")}`;
    $("dhcp-apply-result").textContent = msg;
    await loadDhcp();
  } catch (e) {
    $("dhcp-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("rec-add").addEventListener("click", () => openRecDialog(null));
$("rec-cancel").addEventListener("click", () => $("rec-dialog").close());
$("rec-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    name: f.elements.name.value.trim(),
    type: f.elements.rtype.value,
    value: f.elements.value.value.trim(),
    notes: f.elements.notes.value.trim(),
    enabled: f.elements.enabled.checked,
  };
  try {
    if (editRecUUID) await api("/dns/records/" + editRecUUID, "PUT", body);
    else await api("/dns/records", "POST", body);
    $("rec-dialog").close();
    await loadDns();
  } catch (e) {
    alertErr(e);
  }
});

$("dns-apply").addEventListener("click", async () => {
  const btn = $("dns-apply");
  btn.disabled = true;
  $("dns-apply-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/dns/apply", "POST", {});
    $("dns-apply-result").textContent =
      `Primijenjeno: ${r.applied} zapisa (uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    await loadDns();
  } catch (e) {
    $("dns-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("wg-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    listen_port: parseInt(f.elements.listen_port.value, 10) || 0,
    address: f.elements.address.value.trim(),
    endpoint_host: f.elements.endpoint_host.value.trim(),
    client_dns: f.elements.client_dns.value.trim(),
    client_allowed_ips: f.elements.client_allowed_ips.value.trim(),
  };
  $("wg-server-result").textContent = "Spremam…";
  try {
    const r = await api("/wireguard/server", "POST", body);
    $("wg-server-result").textContent =
      `Spremljeno. Backupi: ${r.backups.join(", ")}`;
    await loadWireguard();
  } catch (e) {
    $("wg-server-result").textContent = "Greška: " + (e.message || e);
  }
});

$("wg-apply").addEventListener("click", async () => {
  const btn = $("wg-apply");
  btn.disabled = true;
  $("wg-apply-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/wireguard/apply", "POST", {});
    $("wg-apply-result").textContent =
      `Primijenjeno: ${r.applied} peerova (uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    await loadWireguard();
  } catch (e) {
    $("wg-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("peer-add").addEventListener("click", () => openPeerDialog(null));
$("peer-cancel").addEventListener("click", () => $("peer-dialog").close());
$("peer-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    name: f.elements.name.value.trim(),
    tunnel_ip: f.elements.tunnel_ip.value.trim(),
    keepalive: parseInt(f.elements.keepalive.value, 10) || 0,
    notes: f.elements.notes.value.trim(),
    enabled: f.elements.enabled.checked,
  };
  if (!editPeerUUID) body.public_key = f.elements.public_key.value.trim();
  try {
    if (editPeerUUID) await api("/wireguard/peers/" + editPeerUUID, "PUT", body);
    else await api("/wireguard/peers", "POST", body);
    $("peer-dialog").close();
    await loadWireguard();
  } catch (e) {
    alertErr(e);
  }
});

$("wgconf-close").addEventListener("click", () => $("wgconf-dialog").close());
$("wgconf-copy").addEventListener("click", async () => {
  const ta = $("wgconf-text");
  try {
    await navigator.clipboard.writeText(ta.value);
    $("wgconf-copy").textContent = "Kopirano ✓";
  } catch {
    ta.select();
    document.execCommand("copy");
    $("wgconf-copy").textContent = "Kopirano ✓";
  }
  setTimeout(() => { $("wgconf-copy").textContent = "Kopiraj"; }, 1500);
});

$("dhcp-toggle").addEventListener("click", async () => {
  const next = !dhcpEnabled;
  const q = next
    ? "Uključiti DHCP poslužitelj na ovom uređaju?\n\nAko u mreži već postoji " +
      "router koji dijeli adrese, klijenti mogu dobivati krive adrese."
    : "Isključiti DHCP poslužitelj? Klijenti će adrese dobivati od postojećeg routera.";
  if (!confirm(q)) return;
  $("dhcp-toggle-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/dhcp/server", "POST", { enabled: next });
    $("dhcp-toggle-result").textContent =
      (r.enabled ? "Uključeno." : "Isključeno.") + " Backup: " + r.backup;
    await loadDhcp();
  } catch (e) {
    $("dhcp-toggle-result").textContent = "Greška: " + (e.message || e);
  }
});

$("pw-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  if (f.elements.new1.value !== f.elements.new2.value) {
    $("pw-result").textContent = "Nove lozinke se ne podudaraju.";
    return;
  }
  $("pw-result").textContent = "Mijenjam…";
  try {
    await api("/auth/password", "POST", {
      current: f.elements.current.value,
      new: f.elements.new1.value,
    });
    f.reset();
    $("pw-result").textContent = "Lozinka promijenjena. Ostale sesije su odjavljene.";
    loadSettings().catch(() => {});
  } catch (e) {
    $("pw-result").textContent = "Greška: " + (e.message || e);
  }
});

$("sess-logout-others").addEventListener("click", async () => {
  try {
    const r = await api("/auth/logout-others", "POST", {});
    $("sess-result").textContent = `Odjavljeno sesija: ${r.removed}`;
    loadSettings().catch(() => {});
  } catch (e) {
    $("sess-result").textContent = "Greška: " + (e.message || e);
  }
});

$("tok-show").addEventListener("click", async () => {
  if (tokVisible) {
    tokVisible = false;
    $("tok-value").textContent = "••••••••••••";
    $("tok-show").textContent = "Prikaži";
    return;
  }
  try {
    const r = await api("/settings/token");
    $("tok-value").textContent = r.token;
    tokVisible = true;
    $("tok-show").textContent = "Sakrij";
  } catch (e) { alertErr(e); }
});

$("tok-regen").addEventListener("click", async () => {
  if (!confirm("Regenerirati API token?\n\nStari token odmah prestaje vrijediti — " +
    "skripte i integracije koje ga koriste treba ažurirati.")) return;
  try {
    const r = await api("/settings/token/regenerate", "POST", {});
    $("tok-value").textContent = r.token;
    tokVisible = true;
    $("tok-show").textContent = "Sakrij";
    $("tok-result").textContent = "Novi token je aktivan — spremi ga na sigurno.";
  } catch (e) {
    $("tok-result").textContent = "Greška: " + (e.message || e);
  }
});

$("bk-create").addEventListener("click", async () => {
  const btn = $("bk-create");
  btn.disabled = true;
  $("bk-create-result").textContent = "Izrađujem backup…";
  try {
    const r = await api("/backup/create", "POST", {});
    $("bk-create-result").textContent =
      `Izrađeno: ${r.archive} (${fmtBytes(r.size_bytes)})`;
    await loadBackup();
  } catch (e) {
    $("bk-create-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("bk-upload").addEventListener("click", async () => {
  const f = $("bk-file").files[0];
  if (!f) { alert("Odaberi .tar.gz arhivu."); return; }
  $("bk-upload-result").textContent = "Učitavam…";
  try {
    const r = await fetch(API + "/backup/upload?name=" + encodeURIComponent(f.name), {
      method: "POST",
      headers: { Authorization: "Bearer " + token },
      body: f,
    });
    const data = await r.json().catch(() => ({}));
    if (r.status === 401) throw { unauthorized: true };
    if (!r.ok) throw new Error(data.error || "HTTP " + r.status);
    $("bk-upload-result").textContent = `Učitano: ${data.archive}`;
    $("bk-file").value = "";
    await loadBackup();
  } catch (e) {
    if (e && e.unauthorized) { logout(true); return; }
    $("bk-upload-result").textContent = "Greška: " + (e.message || e);
  }
});

$("net-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const name of ["ipaddr", "netmask", "gateway", "dns"])
    body[name] = f.elements[name].value.trim();
  if (!confirm(
    `Promijeniti adresu uređaja na ${body.ipaddr}?\n\n` +
    `Veza na trenutnoj adresi će pasti, a browser će te preusmjeriti na:\n` +
    `https://${body.ipaddr}:8443/\n\nTamo se prijavi ponovno istim tokenom.`)) return;
  try {
    const r = await api("/network/lan", "POST", body);
    let n = 8;
    const tick = () => {
      $("net-result").textContent =
        `Primijenjeno (backup: ${r.backup}). Preusmjeravam na ${r.new_url} za ${n} s…`;
      if (n-- <= 0) { location.href = r.new_url; return; }
      setTimeout(tick, 1000);
    };
    stopTimers();
    tick();
  } catch (e) {
    alertErr(e);
  }
});

if (token) start().catch(() => logout(true));
else $("login").classList.remove("hidden");
