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
  const b = h.status === "ok" ? stGood("Sve radi") : stWarn("Dio provjera ne prolazi");
  b.id = "health-badge";
  badge.replaceWith(b);

  const list = $("health-checks");
  list.replaceChildren();
  const items = [
    ["Izlaz prema mreži (gateway)", h.gateway.address || "nepoznat", h.gateway.reachable],
    ["Pretvorba imena u adrese (DNS)", "npr. google.com → IP", h.dns.ok],
    ["Pristup internetu", "provjera prema 1.1.1.1", h.internet.ok],
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

async function loadDhcp() {
  const [st, hs] = await Promise.all([api("/dhcp/status"), api("/inventory/hosts")]);

  const pb = $("dhcp-pool-rows");
  pb.replaceChildren();
  let anyActive = false;
  for (const sv of st.servers || []) {
    if (!sv.ignore) anyActive = true;
    const tr = document.createElement("tr");
    for (const v of [sv.interface,
      sv.start ? `${sv.start} +${sv.limit}` : "—", sv.leasetime || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdS = document.createElement("td");
    tdS.append(sv.ignore ? stOff("Isključen") : stGood("Aktivan"));
    tr.append(tdS);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const tog = document.createElement("button");
    tog.className = "btn-sm" + (sv.ignore ? "" : " danger");
    tog.textContent = sv.ignore ? "Uključi" : "Isključi";
    tog.onclick = async () => {
      const next = !!sv.ignore;
      const q = next
        ? `Uključiti DHCP pool na "${sv.interface}"?\n\nAko u toj mreži već ` +
          "postoji router koji dijeli adrese, klijenti mogu dobivati krive adrese."
        : `Isključiti DHCP pool na "${sv.interface}"?`;
      if (!confirm(q)) return;
      try {
        const r = await api("/dhcp/server", "POST",
          { interface: sv.interface, enabled: next });
        $("dhcp-toggle-result").textContent =
          (r.enabled ? "Uključeno." : "Isključeno.") + " Backup: " + r.backup;
        await loadDhcp();
      } catch (e) {
        $("dhcp-toggle-result").textContent = "Greška: " + (e.message || e);
      }
    };
    tdAct.append(tog);
    tr.append(tdAct);
    pb.append(tr);
  }
  $("dhcp-srv-hint").textContent = anyActive
    ? "⚠ Aktivan DHCP pool na mreži s postojećim routerom može dijeliti krive " +
      "adrese (rogue DHCP)."
    : "Svi DHCP poolovi su isključeni — rezervacije se primjenjuju, ali se ne " +
      "dijele dok pool ne uključiš.";

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
let dnssecOn = false;

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
    ["Provjera potpisa (DNSSEC)", dm.dnssec ? "uključena"
      : dm.dnssec_supported ? "isključena" : "nedostupna (treba dnsmasq-full)"],
  ];
  dnssecOn = !!dm.dnssec;
  $("dnssec-toggle").classList.toggle("hidden", !dm.dnssec_supported);
  $("dnssec-toggle").textContent = dnssecOn
    ? "Isključi DNSSEC" : "Uključi DNSSEC provjeru";
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

/* ---------- firewall ---------- */

let editPfUUID = null;
let editRlUUID = null;

let dmzEnabled = false;

async function loadFirewall() {
  const [st, fw, rl, dmz, n1] = await Promise.all([
    api("/firewall/status"), api("/firewall/forwards"), api("/firewall/rules"),
    api("/firewall/dmz"), api("/firewall/nat11"),
  ]);

  dmzEnabled = dmz.enabled;
  $("dmz-ip").value = dmz.dest_ip || $("dmz-ip").value;
  $("dmz-ip").disabled = dmzEnabled;
  $("dmz-toggle").textContent = dmzEnabled ? "Isključi DMZ" : "Uključi DMZ";
  $("dmz-toggle").className = dmzEnabled ? "primary" : "primary";

  const nb = $("n1-rows");
  nb.replaceChildren();
  for (const n of n1.nat11) {
    const tr = document.createElement("tr");
    for (const v of [n.name, n.public_ip, n.internal_ip]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(n.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openN1Dialog(n)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati 1:1 NAT "${n.name}"?`)) return;
        await api("/firewall/nat11/" + n.uuid, "DELETE").catch(alertErr);
        loadFirewall().catch(alertErr);
      }));
    tr.append(tdAct);
    nb.append(tr);
  }

  const zb = $("zone-rows");
  zb.replaceChildren();
  for (const z of st.zones) {
    const tr = document.createElement("tr");
    for (const v of [z.name, z.input, z.forward, z.masq ? "masq" : "—",
      z.networks.join(", ") || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    zb.append(tr);
  }

  const enN1 = n1.nat11.filter((f) => f.enabled).length;
  const enFw = fw.forwards.filter((f) => f.enabled).length + enN1;
  const enRl = rl.rules.filter((f) => f.enabled).length;
  const devFw = st.redirects.filter((x) => x.managed_by_saguaro).length;
  const devRl = st.rules.filter((x) => x.managed_by_saguaro).length;
  const foreign = st.redirects.length - devFw + st.rules.length - devRl;
  let info = `U bazi: ${enFw} forwarda, ${enRl} pravila · na uređaju: ${devFw} + ${devRl}`;
  if (foreign > 0) info += ` · ostalih (OpenWrt/ručnih): ${foreign}`;
  if (enFw !== devFw || enRl !== devRl) info += " — ⚠ razlika, potrebna primjena";
  $("fw-sync-info").textContent = info;

  const pb = $("pf-rows");
  pb.replaceChildren();
  for (const f of fw.forwards) {
    const tr = document.createElement("tr");
    for (const v of [f.name, f.proto,
      `${f.src_zone}:${f.src_dport}`,
      `${f.dest_ip}${f.dest_port ? ":" + f.dest_port : ""} (${f.dest_zone})`]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(f.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdN = document.createElement("td");
    tdN.textContent = f.notes || "—";
    tr.append(tdN);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openPfDialog(f)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati forward "${f.name}"?`)) return;
        await api("/firewall/forwards/" + f.uuid, "DELETE").catch(alertErr);
        loadFirewall().catch(alertErr);
      }));
    tr.append(tdAct);
    pb.append(tr);
  }

  const rb = $("rl-rows");
  rb.replaceChildren();
  for (const f of rl.rules) {
    const tr = document.createElement("tr");
    const src = f.src_zone + (f.src_ip ? " " + f.src_ip : "");
    const dst = (f.dest_zone || "uređaj") + (f.dest_ip ? " " + f.dest_ip : "") +
      (f.dest_port ? " :" + f.dest_port : "");
    for (const v of [f.name, f.proto, src, dst, f.target]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(f.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openRlDialog(f)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati pravilo "${f.name}"?`)) return;
        await api("/firewall/rules/" + f.uuid, "DELETE").catch(alertErr);
        loadFirewall().catch(alertErr);
      }));
    tr.append(tdAct);
    rb.append(tr);
  }
}

function openPfDialog(f) {
  const d = $("pf-form");
  editPfUUID = f ? f.uuid : null;
  $("pf-dialog-title").textContent = editPfUUID ? "Uredi port forward" : "Novi port forward";
  for (const el of d.elements) {
    if (!el.name) continue;
    if (el.type === "checkbox") el.checked = f ? !!f[el.name] : true;
    else el.value = f ? f[el.name] || "" : "";
  }
  if (!f) d.elements.proto.value = "tcp udp";
  $("pf-dialog").showModal();
}

function openRlDialog(f) {
  const d = $("rl-form");
  editRlUUID = f ? f.uuid : null;
  $("rl-dialog-title").textContent = editRlUUID ? "Uredi pravilo" : "Novo pravilo";
  for (const el of d.elements) {
    if (!el.name) continue;
    if (el.type === "checkbox") el.checked = f ? !!f[el.name] : true;
    else el.value = f ? f[el.name] || "" : "";
  }
  if (!f) { d.elements.proto.value = "tcp udp"; d.elements.target.value = "ACCEPT"; }
  $("rl-dialog").showModal();
}

/* ---------- wireguard ---------- */

let editPeerUUID = null;
let wgAccessMode = "full";
let vpnRulesPeer = null;

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

  wgAccessMode = st.access_mode || "full";
  $("wg-access").textContent = wgAccessMode === "full"
    ? "Prebaci na ograničen pristup" : "Prebaci na pun pristup";
  $("wg-access-hint").textContent = wgAccessMode === "full"
    ? "Pun pristup: svi VPN korisnici vide LAN i internet."
    : "Ograničen pristup: VPN korisnici dosežu samo ono što im dopuštaju " +
      "pravila (gumb Pristup kod peera).";

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
    const acc = document.createElement("button");
    acc.className = "btn-sm";
    acc.textContent = "Pristup";
    acc.onclick = () => openVpnRulesDialog(p);
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
    tdAct.append(acc, edit, del);
    tr.append(tdAct);
    tb.append(tr);
  }
}

let vpnRulesBase = "wireguard/peers"; // ili "openvpn/clients"

async function refreshVpnRules() {
  const x = await api("/" + vpnRulesBase + "/" + vpnRulesPeer.uuid + "/rules");
  const tb = $("vpn-rule-rows");
  tb.replaceChildren();
  for (const rr of x.rules) {
    const tr = document.createElement("tr");
    for (const v of [rr.dest_zone === "*" ? "bilo koja" : rr.dest_zone,
      rr.dest_ip || "sve", rr.dest_port || "svi", rr.proto]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const delBase = vpnRulesBase.startsWith("openvpn") ? "openvpn" : "wireguard";
    tdAct.append(btnSm("Obriši", true, async () => {
      await api("/" + delBase + "/rules/" + rr.uuid, "DELETE").catch(alertErr);
      refreshVpnRules().catch(alertErr);
    }));
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openVpnRulesDialog(p, base) {
  vpnRulesPeer = p;
  vpnRulesBase = base || "wireguard/peers";
  $("vpn-rules-title").textContent = `VPN pristup — ${p.name} (${p.tunnel_ip})`;
  $("vpn-rule-form").reset();
  refreshVpnRules().catch(alertErr);
  $("vpn-rules-dialog").showModal();
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

/* ---------- ospf ---------- */

async function loadOspf() {
  const x = await api("/ospf");
  $("os-enabled").checked = !!x.enabled;
  $("os-rid").value = x.router_id || "";
  $("os-area").value = x.area || "0";

  const chosen = {};
  for (const i of x.interfaces || []) chosen[i.name] = i;
  const box = $("os-ifaces");
  box.replaceChildren();
  for (const av of x.available_interfaces || []) {
    const lab = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.value = av.name; cb.checked = !!chosen[av.name];
    cb.className = "os-if";
    const stub = document.createElement("input");
    stub.type = "checkbox"; stub.className = "os-stub"; stub.dataset.name = av.name;
    stub.checked = chosen[av.name] ? !!chosen[av.name].stub : false;
    const span = document.createElement("span");
    span.textContent = `${av.name} (${av.device}) — `;
    const stubLab = document.createElement("span");
    stubLab.className = "sub";
    stubLab.append("stub: ", stub);
    lab.append(cb, span, stubLab);
    box.append(lab);
  }
  $("os-status").textContent = x.running
    ? (x.status_text || "OSPF radi — nema podataka o susjedima.")
    : x.enabled ? "Servis se pokreće…" : "OSPF je isključen.";
}

/* ---------- blokade (banIP + adblock-fast) ---------- */

async function loadProtection() {
  const x = await api("/protection");

  const bi = x.banip || {};
  $("bi-enabled").checked = !!bi.enabled;
  $("bi-countries").value = bi.countries || "";
  $("bi-allow").value = bi.allow_ips || "";
  const feedBox = $("bi-feeds");
  feedBox.replaceChildren();
  const active = new Set(bi.feeds || []);
  for (const f of bi.available_feeds || []) {
    const lab = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.value = f.id; cb.checked = active.has(f.id);
    lab.append(cb, document.createTextNode(" " + f.label));
    feedBox.append(lab);
  }
  const rt = bi.runtime || {};
  $("bi-status").textContent = !bi.installed
    ? "Paket banip nije instaliran."
    : bi.enabled
      ? "Stanje: " + (rt.status === "active" ? "aktivno" : rt.status || "pokreće se") +
        (rt.element_count ? " · blokiranih zapisa: " + rt.element_count : "") +
        (rt.last_run ? " · zadnja obrada: " + rt.last_run : "")
      : "Blokada IP adresa je isključena.";

  const ad = x.adblock || {};
  $("ad-enabled").checked = !!ad.enabled;
  $("ad-allow").value = ad.allowed_domains || "";
  const entBox = $("ad-entries");
  entBox.replaceChildren();
  for (const e of ad.entries || []) {
    const lab = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.value = e.section; cb.checked = e.enabled;
    const mb = e.size ? " (" + (e.size / 1048576).toFixed(1) + " MB)" : "";
    const span = document.createElement("span");
    span.append(document.createTextNode(e.name));
    const sub = document.createElement("span");
    sub.className = "sub";
    sub.textContent = mb;
    span.append(sub);
    lab.append(cb, span);
    entBox.append(lab);
  }
  $("ad-status").textContent = !ad.installed
    ? "Paket adblock-fast nije instaliran."
    : ad.active_size
      ? "Aktivna lista blokiranih domena: " + fmtBytes(ad.active_size)
      : ad.enabled ? "Uključeno — liste se preuzimaju…" : "Blokada domena je isključena.";
}

/* ---------- multi-wan ---------- */

let mwRules = [];
let mwWanNames = [];

async function loadMultiwan() {
  const x = await api("/multiwan");
  $("mw-enabled").checked = !!x.enabled;
  $("mw-mode").value = x.mode || "failover";
  mwRules = x.rules || [];
  mwWanNames = (x.wans || []).map((w) => w.name);

  const tb = $("mw-wan-rows");
  tb.replaceChildren();
  for (const wn of x.wans || []) {
    const tr = document.createElement("tr");
    tr.dataset.name = wn.name;
    const tdN = document.createElement("td");
    tdN.textContent = wn.name;
    tr.append(tdN);
    const tdE = document.createElement("td");
    const en = document.createElement("input");
    en.type = "checkbox"; en.className = "mw-en"; en.checked = !!wn.enabled;
    tdE.append(en); tr.append(tdE);
    for (const [cls, val, width] of [["mw-pri", wn.priority, 60],
      ["mw-w", wn.weight, 60], ["mw-track", wn.track_ips, 170]]) {
      const td = document.createElement("td");
      const inp = document.createElement("input");
      inp.className = cls; inp.value = val; inp.style.width = width + "px";
      td.append(inp); tr.append(td);
    }
    tb.append(tr);
  }

  renderMwRules();

  const sb = $("mw-status-rows");
  sb.replaceChildren();
  const ifaces = (x.status && x.status.interfaces) || {};
  for (const [name, st] of Object.entries(ifaces)) {
    if (!mwWanNames.includes(name)) continue;
    const tr = document.createElement("tr");
    const tdN = document.createElement("td"); tdN.textContent = name; tr.append(tdN);
    const tdS = document.createElement("td");
    tdS.append(st.status === "online" ? stGood("Radi")
      : st.status === "offline" ? stCrit("Pala")
      : stOff(st.status === "disabled" ? "Isključena" : st.status));
    tr.append(tdS);
    const tdP = document.createElement("td");
    tdP.textContent = (st.track_ip || [])
      .map((t) => `${t.ip}: ${t.status === "up" ? t.latency + " ms" : t.status}`)
      .join(" · ") || "—";
    tr.append(tdP);
    sb.append(tr);
  }
  $("mw-status-hint").textContent = x.managed
    ? "" : "Multi-WAN još nije konfiguriran kroz Saguaro — spremi postavke lijevo.";
}

function renderMwRules() {
  const tb = $("mwr-rows");
  tb.replaceChildren();
  mwRules.forEach((r, i) => {
    const tr = document.createElement("tr");
    for (const v of [r.label, r.src_ip || "svi", r.dest_ip || "sva",
      r.dest_port || "svi", r.proto || "svi", r.use_wan]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(btnSm("Ukloni", true, () => {
      mwRules.splice(i, 1);
      renderMwRules();
    }));
    tr.append(tdAct);
    tb.append(tr);
  });
}

/* ---------- openvpn ---------- */

let editOvcUUID = null;
let ovAccessMode = "full";

async function loadOpenvpn() {
  const [st, cl] = await Promise.all([
    api("/openvpn/status"), api("/openvpn/clients"),
  ]);

  const srv = st.server || {};
  const f = $("ov-form");
  if (srv.configured) {
    f.elements.port.value = srv.port || "";
    f.elements.network.value = srv.network || "";
    f.elements.endpoint_host.value = srv.endpoint_host || "";
    f.elements.client_dns.value = srv.client_dns || "";
    f.elements.push_lan.checked = !!srv.push_lan;
  }

  const kv = $("ov-kv");
  kv.replaceChildren();
  const rows = [
    ["Paket", st.installed ? "instaliran" : "nedostaje (openvpn-openssl)"],
    ["Poslužitelj", st.running ? "radi" : srv.configured ? "ne radi" : "nije postavljen"],
    ["Mreža tunela", srv.network || "—"],
    ["Trenutno spojeno", String((st.connected || []).length)],
  ];
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }

  ovAccessMode = st.access_mode || "full";
  $("ov-access").textContent = ovAccessMode === "full"
    ? "Prebaci na ograničen pristup" : "Prebaci na pun pristup";
  $("ov-access-hint").textContent = ovAccessMode === "full"
    ? "Pun pristup: svi VPN korisnici vide LAN i internet."
    : "Ograničen pristup: korisnici dosežu samo ono što im dopuštaju " +
      "pravila (gumb Pristup).";

  const connected = {};
  for (const c of st.connected || []) connected[c.name] = c;

  const tb = $("ovc-rows");
  tb.replaceChildren();
  for (const c of cl.clients) {
    const live = connected[c.name];
    const tr = document.createElement("tr");
    for (const v of [c.name, c.tunnel_ip]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(c.enabled ? stGood("Da") : stOff("Ne"));
    tr.append(tdE);
    const tdC = document.createElement("td");
    tdC.append(live ? stGood("Spojen (" + live.real_addr.split(":")[0] + ")")
      : stOff("Nije spojen"));
    tr.append(tdC);
    const tdT = document.createElement("td");
    tdT.textContent = live ? fmtBytes(live.rx_bytes) + " / " + fmtBytes(live.tx_bytes) : "—";
    tr.append(tdT);

    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Config", false, async () => {
        try {
          const x = await api("/openvpn/clients/" + c.uuid + "/config");
          $("wgconf-title").textContent = "OpenVPN config — " + x.name + ".ovpn";
          $("wgconf-text").value = x.config;
          $("wgconf-dialog").showModal();
        } catch (e) { alertErr(e); }
      }),
      btnSm("Pristup", false, () => openVpnRulesDialog(c, "openvpn/clients")),
      btnSm("Uredi", false, () => openOvcDialog(c)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati klijenta "${c.name}"? Njegov certifikat ` +
          "prestaje vrijediti nakon primjene.")) return;
        await api("/openvpn/clients/" + c.uuid, "DELETE").catch(alertErr);
        loadOpenvpn().catch(alertErr);
      }));
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openOvcDialog(c) {
  const f = $("ovc-form");
  editOvcUUID = c ? c.uuid : null;
  $("ovc-dialog-title").textContent = editOvcUUID ? "Uredi klijenta" : "Novi klijent";
  f.elements.name.value = c ? c.name : "";
  f.elements.name.disabled = !!editOvcUUID; // naziv je CN certifikata
  f.elements.tunnel_ip.value = c ? c.tunnel_ip : "";
  f.elements.notes.value = c ? c.notes || "" : "";
  f.elements.enabled.checked = c ? !!c.enabled : true;
  $("ovc-dialog").showModal();
}

/* ---------- ažuriranje ---------- */

let upHasStaged = false;
let upHasLatest = false;

async function loadUpdate() {
  const x = await api("/update/status");
  const kv = $("up-kv");
  kv.replaceChildren();
  const rows = [["Instalirana verzija", "v" + x.current]];
  if (x.latest && x.latest.tag) {
    rows.push(["Zadnje izdanje na GitHubu", x.latest.tag +
      (x.latest.asset ? ` (${x.latest.asset})` : "")]);
    upHasLatest = !!x.latest.asset;
  } else if (x.github_error) {
    rows.push(["GitHub", "provjera nije uspjela"]);
    upHasLatest = false;
  } else {
    rows.push(["Zadnje izdanje na GitHubu", "još nema objavljenih izdanja"]);
    upHasLatest = false;
  }
  if (x.staged) {
    rows.push(["Učitan paket", fmtBytes(x.staged.size_bytes) + " · " +
      new Date(x.staged.uploaded_at * 1000).toLocaleString("hr-HR")]);
  }
  upHasStaged = !!x.staged;
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
  $("up-github").classList.toggle("hidden", !upHasLatest);
  $("up-apply").classList.toggle("hidden", !upHasStaged);
  $("up-github-note").textContent = upHasLatest
    ? "Nadogradnja preuzima paket, radi puni backup i ponovno pokreće servis."
    : "";
}

async function applyUpdate(source) {
  $("up-result").textContent = "Nadograđujem (backup + zamjena)…";
  try {
    const r = await api("/update/apply", "POST", { source });
    stopTimers();
    $("up-result").textContent =
      `Nadogradnja primijenjena (backup: ${r.backup}). Servis se ponovno ` +
      "pokreće — osvježi stranicu za ~10 sekundi.";
  } catch (e) {
    $("up-result").textContent = "Greška: " + (e.message || e);
  }
}

/* ---------- postavke ---------- */

let tokVisible = false;

async function loadSettings() {
  const [s, sys] = await Promise.all([
    api("/auth/session"), api("/settings/system"),
  ]);
  const sl = sys.syslog || {};
  $("sl-enabled").checked = !!sl.enabled;
  $("sl-host").value = sl.host || "";
  $("sl-port").value = sl.port || "514";
  $("sl-proto").value = sl.proto || "udp";
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
  const [x, sch] = await Promise.all([
    api("/backup/archives"), api("/backup/schedule"),
  ]);
  $("bs-enabled").checked = !!sch.enabled;
  $("bs-freq").value = sch.freq || "daily";

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

let editWanName = null; // null = novi (auto sag_wanN)
let wanDevices = [];
let wanNames = [];

const ACCESS_LABEL = { wan: "internet", wan_lan: "internet + LAN", isolated: "izolirano" };

async function loadNetwork() {
  const [x, ws, vl] = await Promise.all([
    api("/network/lan"), api("/network/wans"), api("/network/vlans")]);

  const vb = $("vlan-rows");
  vb.replaceChildren();
  for (const v of vl.vlans) {
    const tr = document.createElement("tr");
    for (const c of [v.vid, v.name || "—", v.port,
      v.ipaddr ? `${v.ipaddr} (${v.netmask})` : "—",
      v.dhcp ? `${v.dhcp_start} +${v.dhcp_limit}` : "isključen",
      ACCESS_LABEL[v.access] || v.access]) {
      const td = document.createElement("td");
      td.textContent = c;
      tr.append(td);
    }
    const tdS = document.createElement("td");
    tdS.append(v.up ? stGood("Aktivno") : stOff("Neaktivno"));
    tr.append(tdS);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(btnSm("Obriši", true, async () => {
      if (!confirm(`Obrisati VLAN ${v.vid} (${v.name})?\n\nUklanja sučelje, ` +
        "DHCP pool i firewall zonu te mreže.")) return;
      try {
        await api("/network/vlans/" + v.vid, "DELETE");
        $("vlan-result").textContent = `VLAN ${v.vid} obrisan.`;
        await loadNetwork();
      } catch (e) { alertErr(e); }
    }));
    tr.append(tdAct);
    vb.append(tr);
  }
  const f = $("net-form");
  for (const name of ["ipaddr", "netmask", "gateway", "dns"])
    f.elements[name].value = x[name] || "";

  wanDevices = ws.devices || [];
  wanNames = ws.wans.map((w) => w.name);
  const tb = $("wan-rows");
  tb.replaceChildren();
  for (const wn of ws.wans) {
    const tr = document.createElement("tr");
    for (const v of [wn.name, wn.proto, wn.device || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdS = document.createElement("td");
    tdS.append(wn.up ? stGood("Aktivno") : stOff("Neaktivno"));
    tr.append(tdS);
    for (const v of [
      (wn.runtime_ipv4 && wn.runtime_ipv4.length ? wn.runtime_ipv4
        : wn.ipaddrs).join(", ") || "—",
      wn.gateway || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(btnSm("Uredi", false, () => openWanDialog(wn)));
    if (wn.name !== "wan") {
      tdAct.append(btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati WAN "${wn.name}"?`)) return;
        try {
          await api("/network/wans/" + wn.name, "DELETE");
          await loadNetwork();
        } catch (e) { alertErr(e); }
      }));
    }
    tr.append(tdAct);
    tb.append(tr);
  }
}

function wanProtoFields() {
  const p = $("wan-proto").value;
  for (const el of document.querySelectorAll("#wan-form .wan-static"))
    el.classList.toggle("hidden", p !== "static" && !el.classList.contains("wan-dhcp"));
  for (const el of document.querySelectorAll("#wan-form .wan-dhcp"))
    el.classList.toggle("hidden", p === "pppoe");
  for (const el of document.querySelectorAll("#wan-form .wan-pppoe"))
    el.classList.toggle("hidden", p !== "pppoe");
}

function openWanDialog(wn) {
  const f = $("wan-form");
  editWanName = wn ? wn.name : null;
  $("wan-dialog-title").textContent = wn ? "Uredi " + wn.name : "Novi WAN";
  const devSel = $("wan-device");
  devSel.replaceChildren();
  for (const d of wanDevices) {
    const o = document.createElement("option");
    o.value = d.name;
    let label = d.name + (d.carrier ? " (link)" : " (nema linka)");
    if (d.used_by && (!wn || d.name !== wn.device)) label += " — koristi " + d.used_by;
    o.textContent = label;
    devSel.append(o);
  }
  f.elements.proto.value = wn ? wn.proto : "dhcp";
  if (wn && wn.device) f.elements.device.value = wn.device;
  f.elements.ipaddrs.value = wn ? (wn.ipaddrs || []).join(" ") : "";
  f.elements.gateway.value = wn ? wn.gateway || "" : "";
  f.elements.dns.value = wn ? (wn.dns || []).join(" ") : "";
  f.elements.username.value = wn ? wn.username || "" : "";
  f.elements.password.value = "";
  wanProtoFields();
  $("wan-dialog").showModal();
}

/* ---------- navigacija i router ---------- */

// Moduli: id → [naslov, opis, loader]. Lijeva traka prikazuje grupe;
// moduli aktivne grupe su tabovi iznad sadržaja (uzor: Saguaro Network Manager).
const MODULES = {
  dashboard: ["Dashboard", "Pregled stanja uređaja i mreže", () => null],
  network:   ["Mreža", "LAN adresa, WAN veze i VLAN mreže", () => loadNetwork()],
  multiwan:  ["Multi-WAN", "Više internet veza — failover, raspodjela i nadzor", () => loadMultiwan()],
  ospf:      ["OSPF", "Dinamičko usmjeravanje — automatska razmjena ruta s routerima", () => loadOspf()],
  dhcp:      ["DHCP", "Dodjela IP adresa i rezervacije za uređaje u mreži", () => loadDhcp()],
  dns:       ["DNS", "Lokalna imena uređaja (npr. nas.lan umjesto IP adrese)", () => loadDns()],
  firewall:  ["Firewall", "Pravila prometa, port forwardi, DMZ i 1:1 NAT", () => loadFirewall()],
  protection: ["Blokade", "Blokiranje zloćudnih IP adresa i reklamnih/malware domena", () => loadProtection()],
  wireguard: ["WireGuard", "Udaljeni pristup — moderni VPN s ključevima", () => loadWireguard()],
  openvpn:   ["OpenVPN", "Udaljeni pristup — klasični VPN s certifikatima", () => loadOpenvpn()],
  devices:   ["Uređaji", "Inventar opreme — ovaj uređaj i susjedni", () => loadDevices()],
  backup:    ["Backup", "Sigurnosne kopije uređaja i vraćanje", () => loadBackup()],
  update:    ["Ažuriranje", "Nadogradnja Saguaro sustava uz automatski backup", () => loadUpdate()],
  settings:  ["Postavke", "Korisnički račun, sesije, API token i logovi", () => loadSettings()],
  help:      ["Pomoć", "Upute za rad — kako koristiti svaki modul", () => null],
};
const NAV_GROUPS = [
  ["Status", ["dashboard"]],
  ["Mreža", ["network", "multiwan", "ospf", "dhcp", "dns"]],
  ["Zaštita", ["firewall", "protection"]],
  ["VPN", ["wireguard", "openvpn"]],
  ["Sustav", ["devices", "backup", "update", "settings", "help"]],
];
const groupOf = (id) => NAV_GROUPS.findIndex((g) => g[1].includes(id));
const lastByGroup = {};

function renderNav(active) {
  const nav = $("nav");
  nav.replaceChildren();
  const gi = groupOf(active);
  NAV_GROUPS.forEach((g, i) => {
    const b = document.createElement("button");
    b.className = "nav-cat" + (i === gi ? " active" : "");
    b.textContent = g[0];
    b.onclick = () => {
      const target = lastByGroup[i] && g[1].includes(lastByGroup[i])
        ? lastByGroup[i] : g[1][0];
      location.hash = "#/" + (target === "dashboard" ? "" : target);
    };
    nav.append(b);
  });

  const sub = $("subnav");
  const ids = NAV_GROUPS[gi][1];
  if (ids.length <= 1) {
    sub.classList.add("hidden");
    sub.replaceChildren();
  } else {
    sub.classList.remove("hidden");
    sub.replaceChildren();
    for (const id of ids) {
      const b = document.createElement("button");
      b.className = "subtab" + (id === active ? " active" : "");
      b.textContent = MODULES[id][0];
      b.onclick = () => { location.hash = "#/" + id; };
      sub.append(b);
    }
  }
}

function route() {
  let view = location.hash.replace(/^#\/?/, "").split("/")[0];
  if (!MODULES[view]) view = "dashboard";
  lastByGroup[groupOf(view)] = view;
  for (const v of Object.keys(MODULES))
    $("view-" + v).classList.toggle("hidden", v !== view);
  $("page-title").textContent = MODULES[view][0];
  $("page-desc").textContent = MODULES[view][1];
  renderNav(view);
  if (!token) return;
  const load = MODULES[view][2]();
  if (load) load.catch(alertErr);
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
  drawSparks().catch(() => {});
}

// drawSparks crta male grafove zadnjih sat vremena u pločicama CPU/RAM
async function drawSparks() {
  const x = await api("/metrics/history");
  const samples = x.samples || [];
  const draw = (id, vals, max) => {
    const svg = $(id);
    if (!svg) return;
    svg.replaceChildren();
    if (vals.length < 2) return;
    const top = Math.max(max, ...vals) || 1;
    const pts = vals.map((v, i) =>
      `${(i / (vals.length - 1)) * 100},${23 - (v / top) * 22}`).join(" ");
    const pl = document.createElementNS("http://www.w3.org/2000/svg", "polyline");
    pl.setAttribute("points", pts);
    svg.append(pl);
  };
  draw("spark-cpu", samples.map((s) => s.load1), cores);
  draw("spark-ram", samples.map((s) => s.mem_pct), 100);
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

$("os-save").addEventListener("click", async () => {
  const interfaces = [];
  for (const cb of $("os-ifaces").querySelectorAll(".os-if:checked")) {
    const stub = $("os-ifaces").querySelector(`.os-stub[data-name="${cb.value}"]`);
    interfaces.push({ name: cb.value, stub: stub ? stub.checked : false });
  }
  $("os-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/ospf", "POST", {
      enabled: $("os-enabled").checked,
      router_id: $("os-rid").value.trim(),
      area: $("os-area").value.trim(),
      interfaces,
    });
    $("os-result").textContent = r.enabled
      ? "OSPF uključen (router ID " + r.router_id + ")." : "OSPF isključen.";
    setTimeout(() => loadOspf().catch(() => {}), 3000);
  } catch (e) {
    $("os-result").textContent = "Greška: " + (e.message || e);
  }
});
$("os-refresh").addEventListener("click", () => loadOspf().catch(alertErr));

$("pub-wizard").addEventListener("click", async () => {
  const dl = $("pub-hosts");
  dl.replaceChildren();
  try {
    const hs = await api("/inventory/hosts");
    for (const h of hs.hosts.filter((h) => h.ipv4)) {
      const o = document.createElement("option");
      o.value = h.ipv4;
      o.label = h.hostname || h.mac;
      dl.append(o);
    }
  } catch { /* inventar nije obavezan */ }
  $("pub-form").reset();
  $("pub-dialog").showModal();
});
$("pub-cancel").addEventListener("click", () => $("pub-dialog").close());
$("pub-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const ip = f.elements.dest_ip.value.trim();
  const prefix = f.elements.prefix.value.trim();
  const srcDip = f.elements.src_dip.value.trim();
  const refl = f.elements.reflection.checked;
  const jobs = [];
  for (const cb of $("pub-services").querySelectorAll("input:checked")) {
    const [port, proto, svc] = cb.value.split(":");
    jobs.push({ name: prefix + "-" + svc, proto, src_dport: port });
  }
  for (const p of f.elements.custom.value.trim().split(/[\s,]+/).filter(Boolean)) {
    jobs.push({ name: prefix + "-port" + p.replace("-", "do"), proto: "tcp udp", src_dport: p });
  }
  if (!jobs.length) { alert("Odaberi bar jednu uslugu ili port."); return; }
  try {
    for (const j of jobs) {
      await api("/firewall/forwards", "POST", {
        ...j, dest_ip: ip, src_dip: srcDip, reflection: refl,
        notes: "čarobnjak: objava servera " + prefix,
      });
    }
    $("pub-dialog").close();
    $("fw-apply-result").textContent =
      `Čarobnjak je stvorio ${jobs.length} forwarda — klikni "Primijeni firewall".`;
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

$("pf-add").addEventListener("click", () => openPfDialog(null));
$("pf-cancel").addEventListener("click", () => $("pf-dialog").close());
$("pf-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const n of ["name", "proto", "src_zone", "src_dport", "dest_zone",
    "dest_ip", "dest_port", "src_dip", "notes"]) body[n] = f.elements[n].value.trim();
  body.enabled = f.elements.enabled.checked;
  body.reflection = f.elements.reflection.checked;
  try {
    if (editPfUUID) await api("/firewall/forwards/" + editPfUUID, "PUT", body);
    else await api("/firewall/forwards", "POST", body);
    $("pf-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

$("rl-add").addEventListener("click", () => openRlDialog(null));
$("rl-cancel").addEventListener("click", () => $("rl-dialog").close());
$("rl-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const n of ["name", "proto", "src_zone", "src_ip", "dest_zone",
    "dest_ip", "dest_port", "target", "notes"]) body[n] = f.elements[n].value.trim();
  body.enabled = f.elements.enabled.checked;
  try {
    if (editRlUUID) await api("/firewall/rules/" + editRlUUID, "PUT", body);
    else await api("/firewall/rules", "POST", body);
    $("rl-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

$("fw-apply").addEventListener("click", async () => {
  const btn = $("fw-apply");
  btn.disabled = true;
  $("fw-apply-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/firewall/apply", "POST", {});
    $("fw-apply-result").textContent =
      `Primijenjeno: ${r.applied_forwards} forwarda + ${r.applied_rules} pravila ` +
      `(uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    await loadFirewall();
  } catch (e) {
    $("fw-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("dmz-toggle").addEventListener("click", async () => {
  const next = !dmzEnabled;
  const ip = $("dmz-ip").value.trim();
  if (next && !ip) { alert("Upiši IP adresu DMZ hosta."); return; }
  if (next && !confirm(`Uključiti DMZ prema ${ip}?\n\nTaj host prima SAV ` +
    "dolazni promet s interneta koji nije uhvaćen drugim pravilima.")) return;
  try {
    const r = await api("/firewall/dmz", "POST", { enabled: next, dest_ip: ip });
    $("dmz-result").textContent = r.enabled
      ? `DMZ aktivan prema ${r.dest_ip}. Backup: ${r.backup}`
      : "DMZ isključen." + (r.backup ? " Backup: " + r.backup : "");
    await loadFirewall();
  } catch (e) {
    $("dmz-result").textContent = "Greška: " + (e.message || e);
  }
});

let editN1UUID = null;
function openN1Dialog(n) {
  const f = $("n1-form");
  editN1UUID = n ? n.uuid : null;
  $("n1-dialog-title").textContent = editN1UUID ? "Uredi 1:1 NAT" : "Novi 1:1 NAT";
  for (const el of f.elements) {
    if (!el.name) continue;
    if (el.type === "checkbox") el.checked = n ? !!n[el.name] : true;
    else el.value = n ? n[el.name] || "" : "";
  }
  $("n1-dialog").showModal();
}
$("n1-add").addEventListener("click", () => openN1Dialog(null));
$("n1-cancel").addEventListener("click", () => $("n1-dialog").close());
$("n1-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const n of ["name", "zone", "public_ip", "internal_ip", "notes"])
    body[n] = f.elements[n].value.trim();
  body.enabled = f.elements.enabled.checked;
  try {
    if (editN1UUID) await api("/firewall/nat11/" + editN1UUID, "PUT", body);
    else await api("/firewall/nat11", "POST", body);
    $("n1-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

$("vlan-add").addEventListener("click", () => {
  const sel = $("vlan-port");
  sel.replaceChildren();
  for (const d of wanDevices) {
    const o = document.createElement("option");
    o.value = d.name;
    o.textContent = d.name + (d.used_by ? " — koristi " + d.used_by : "") +
      (d.carrier ? " (link)" : "");
    sel.append(o);
  }
  $("vlan-form").reset();
  $("vlan-dialog").showModal();
});
$("vlan-cancel").addEventListener("click", () => $("vlan-dialog").close());
$("vlan-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    vid: parseInt(f.elements.vid.value, 10) || 0,
    port: f.elements.port.value,
    name: f.elements.name.value.trim(),
    cidr: f.elements.cidr.value.trim(),
    dhcp: f.elements.dhcp.checked,
    dhcp_start: parseInt(f.elements.dhcp_start.value, 10) || 0,
    dhcp_limit: parseInt(f.elements.dhcp_limit.value, 10) || 0,
    dhcp_leasetime: f.elements.dhcp_leasetime.value.trim(),
    access: f.elements.access.value,
  };
  try {
    const r = await api("/network/vlans", "POST", body);
    $("vlan-dialog").close();
    $("vlan-result").textContent =
      `Stvoreno: ${r.created} na ${r.device}. Backupi: ${r.backups.join(", ")}`;
    await loadNetwork();
  } catch (e) { alertErr(e); }
});

$("wg-access").addEventListener("click", async () => {
  const next = wgAccessMode === "full" ? "restricted" : "full";
  const q = next === "restricted"
    ? "Prebaciti na OGRANIČEN pristup?\n\nVPN korisnici gube pristup svemu " +
      "osim onoga što im izričito dopustiš pravilima (gumb Pristup), " +
      "nakon sljedeće primjene peerova."
    : "Prebaciti na PUN pristup?\n\nSvi VPN korisnici dobivaju pristup " +
      "cijelom LAN-u i internetu.";
  if (!confirm(q)) return;
  try {
    const r = await api("/wireguard/access", "POST", { mode: next });
    $("wg-apply-result").textContent = "Način pristupa: " + r.mode +
      (r.backup ? ". Backup: " + r.backup : "");
    await loadWireguard();
  } catch (e) { alertErr(e); }
});

$("vpn-rules-close").addEventListener("click", () => $("vpn-rules-dialog").close());
$("vpn-rule-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    dest_zone: f.elements.dest_zone.value,
    dest_ip: f.elements.dest_ip.value.trim(),
    dest_port: f.elements.dest_port.value.trim(),
    proto: f.elements.proto.value,
  };
  try {
    await api("/" + vpnRulesBase + "/" + vpnRulesPeer.uuid + "/rules", "POST", body);
    f.elements.dest_ip.value = "";
    f.elements.dest_port.value = "";
    await refreshVpnRules();
  } catch (e) { alertErr(e); }
});

$("wan-add").addEventListener("click", () => openWanDialog(null));
$("wan-cancel").addEventListener("click", () => $("wan-dialog").close());
$("wan-proto").addEventListener("change", wanProtoFields);
$("wan-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  let name = editWanName;
  if (!name) {
    for (let i = 2; i <= 9; i++) {
      if (!wanNames.includes("sag_wan" + i)) { name = "sag_wan" + i; break; }
    }
    if (!name) { alert("Iskorišteni su svi WAN slotovi."); return; }
  }
  const body = {
    proto: f.elements.proto.value,
    device: f.elements.device.value,
    ipaddrs: f.elements.ipaddrs.value.trim(),
    gateway: f.elements.gateway.value.trim(),
    dns: f.elements.dns.value.trim(),
    username: f.elements.username.value.trim(),
    password: f.elements.password.value,
  };
  if (name === "wan" && !confirm(
    "Mijenjaš glavni WAN. Kriva postavka može prekinuti internet uređaja. Nastaviti?"))
    return;
  try {
    const r = await api("/network/wans/" + name, "POST", body);
    $("wan-dialog").close();
    $("wan-result").textContent =
      `Primijenjeno na ${r.applied}. Backupi: ${r.backups.join(", ")}`;
    await loadNetwork();
  } catch (e) { alertErr(e); }
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

$("dnssec-toggle").addEventListener("click", async () => {
  const next = !dnssecOn;
  if (next && !confirm("Uključiti DNSSEC provjeru potpisa?\n\nDomene s krivo " +
    "postavljenim potpisima prestat će se otvarati (to je i svrha zaštite).")) return;
  try {
    const r = await api("/dns/dnssec", "POST", { dnssec: next });
    $("dnssec-result").textContent =
      (r.dnssec ? "DNSSEC uključen." : "DNSSEC isključen.") + " Backup: " + r.backup;
    await loadDns();
  } catch (e) {
    $("dnssec-result").textContent = "Greška: " + (e.message || e);
  }
});

$("bi-save").addEventListener("click", async () => {
  const feeds = [...$("bi-feeds").querySelectorAll("input:checked")].map((c) => c.value);
  $("bi-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/protection/banip", "POST", {
      enabled: $("bi-enabled").checked,
      feeds,
      countries: $("bi-countries").value.trim(),
      allow_ips: $("bi-allow").value.trim(),
    });
    $("bi-result").textContent = (r.enabled
      ? "Uključeno — " + r.note + "." : "Isključeno.") + " Backup: " + r.backup;
    setTimeout(() => loadProtection().catch(() => {}), 4000);
  } catch (e) {
    $("bi-result").textContent = "Greška: " + (e.message || e);
  }
});

$("ad-save").addEventListener("click", async () => {
  const sections = [...$("ad-entries").querySelectorAll("input:checked")].map((c) => c.value);
  $("ad-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/protection/adblock", "POST", {
      enabled: $("ad-enabled").checked,
      sections,
      allowed_domains: $("ad-allow").value.trim(),
    });
    $("ad-result").textContent = (r.enabled
      ? "Uključeno — " + r.note + "." : "Isključeno.") + " Backup: " + r.backup;
    setTimeout(() => loadProtection().catch(() => {}), 4000);
  } catch (e) {
    $("ad-result").textContent = "Greška: " + (e.message || e);
  }
});

$("mw-save").addEventListener("click", async () => {
  const wans = [];
  for (const tr of $("mw-wan-rows").children) {
    wans.push({
      name: tr.dataset.name,
      enabled: tr.querySelector(".mw-en").checked,
      priority: parseInt(tr.querySelector(".mw-pri").value, 10) || 1,
      weight: parseInt(tr.querySelector(".mw-w").value, 10) || 1,
      track_ips: tr.querySelector(".mw-track").value.trim(),
    });
  }
  const body = {
    enabled: $("mw-enabled").checked,
    mode: $("mw-mode").value,
    wans, rules: mwRules,
  };
  $("mw-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/multiwan", "POST", body);
    $("mw-result").textContent = (r.enabled
      ? `Multi-WAN aktivan (${r.mode === "failover" ? "failover" : "raspodjela"}).`
      : "Multi-WAN isključen.") + " Backup: " + r.backup;
    await loadMultiwan();
  } catch (e) {
    $("mw-result").textContent = "Greška: " + (e.message || e);
  }
});

$("mwr-add").addEventListener("click", () => {
  const sel = $("mwr-wan");
  sel.replaceChildren();
  for (const n of mwWanNames) {
    const o = document.createElement("option");
    o.value = n; o.textContent = n;
    sel.append(o);
  }
  $("mwr-form").reset();
  $("mwr-dialog").showModal();
});
$("mwr-cancel").addEventListener("click", () => $("mwr-dialog").close());
$("mwr-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const f = ev.target;
  mwRules.push({
    label: f.elements.label.value.trim().toLowerCase(),
    src_ip: f.elements.src_ip.value.trim(),
    dest_ip: f.elements.dest_ip.value.trim(),
    dest_port: f.elements.dest_port.value.trim(),
    proto: f.elements.proto.value,
    use_wan: f.elements.use_wan.value,
  });
  renderMwRules();
  $("mwr-dialog").close();
});

$("ov-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    port: parseInt(f.elements.port.value, 10) || 0,
    network: f.elements.network.value.trim(),
    endpoint_host: f.elements.endpoint_host.value.trim(),
    client_dns: f.elements.client_dns.value.trim(),
    push_lan: f.elements.push_lan.checked,
  };
  $("ov-server-result").textContent = "Spremam (prvi put traje par sekundi — izdaju se certifikati)…";
  try {
    const r = await api("/openvpn/server", "POST", body);
    $("ov-server-result").textContent = "Spremljeno. Backupi: " + r.backups.join(", ");
    await loadOpenvpn();
  } catch (e) {
    $("ov-server-result").textContent = "Greška: " + (e.message || e);
  }
});

$("ov-apply").addEventListener("click", async () => {
  const btn = $("ov-apply");
  btn.disabled = true;
  $("ov-apply-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/openvpn/apply", "POST", {});
    $("ov-apply-result").textContent =
      `Primijenjeno: ${r.applied_clients} klijenata, ${r.applied_rules} pravila. ` +
      `Backup: ${r.backup}`;
    await loadOpenvpn();
  } catch (e) {
    $("ov-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

$("ov-access").addEventListener("click", async () => {
  const next = ovAccessMode === "full" ? "restricted" : "full";
  const q = next === "restricted"
    ? "Prebaciti na OGRANIČEN pristup? Korisnici gube pristup svemu osim " +
      "onoga što im dopustiš pravilima."
    : "Prebaciti na PUN pristup? Svi VPN korisnici vide LAN i internet.";
  if (!confirm(q)) return;
  try {
    const r = await api("/openvpn/access", "POST", { mode: next });
    $("ov-apply-result").textContent = "Način pristupa: " + r.mode;
    await loadOpenvpn();
  } catch (e) { alertErr(e); }
});

$("ovc-add").addEventListener("click", () => openOvcDialog(null));
$("ovc-cancel").addEventListener("click", () => $("ovc-dialog").close());
$("ovc-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    tunnel_ip: f.elements.tunnel_ip.value.trim(),
    notes: f.elements.notes.value.trim(),
    enabled: f.elements.enabled.checked,
  };
  if (!editOvcUUID) body.name = f.elements.name.value.trim();
  try {
    if (editOvcUUID) await api("/openvpn/clients/" + editOvcUUID, "PUT", body);
    else await api("/openvpn/clients", "POST", body);
    $("ovc-dialog").close();
    await loadOpenvpn();
  } catch (e) { alertErr(e); }
});

$("bs-save").addEventListener("click", async () => {
  try {
    const r = await api("/backup/schedule", "POST", {
      enabled: $("bs-enabled").checked, freq: $("bs-freq").value,
    });
    $("bs-result").textContent = r.enabled
      ? "Raspored uključen (" + (r.freq === "weekly" ? "tjedno" : "dnevno") + ")."
      : "Raspored isključen.";
  } catch (e) {
    $("bs-result").textContent = "Greška: " + (e.message || e);
  }
});

$("sl-save").addEventListener("click", async () => {
  try {
    const r = await api("/settings/syslog", "POST", {
      enabled: $("sl-enabled").checked,
      host: $("sl-host").value.trim(),
      port: parseInt($("sl-port").value, 10) || 0,
      proto: $("sl-proto").value,
    });
    $("sl-result").textContent = r.enabled
      ? "Logovi se šalju. Backup: " + r.backup : "Slanje logova isključeno.";
  } catch (e) {
    $("sl-result").textContent = "Greška: " + (e.message || e);
  }
});

$("up-upload").addEventListener("click", async () => {
  const f = $("up-file").files[0];
  if (!f) { alert("Odaberi .tar.gz paket."); return; }
  $("up-result").textContent = "Učitavam…";
  try {
    const r = await fetch(API + "/update/upload", {
      method: "POST",
      headers: { Authorization: "Bearer " + token },
      body: f,
    });
    const data = await r.json().catch(() => ({}));
    if (r.status === 401) throw { unauthorized: true };
    if (!r.ok) throw new Error(data.error || "HTTP " + r.status);
    $("up-result").textContent = "Paket učitan (" + fmtBytes(data.size_bytes) + ").";
    $("up-file").value = "";
    await loadUpdate();
  } catch (e) {
    if (e && e.unauthorized) { logout(true); return; }
    $("up-result").textContent = "Greška: " + (e.message || e);
  }
});

$("up-apply").addEventListener("click", () => {
  if (!confirm("Primijeniti učitani paket? Radi se backup pa restart servisa.")) return;
  applyUpdate("staged");
});
$("up-github").addEventListener("click", () => {
  if (!confirm("Preuzeti i primijeniti zadnje izdanje s GitHuba?\n\n" +
    "Radi se backup pa restart servisa.")) return;
  applyUpdate("github");
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
