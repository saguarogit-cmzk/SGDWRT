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
// setNote piše kratku napomenu uz naslov ploče; cijeli tekst ostaje u oblačiću
// jer se u uskoj traci skraćuje
function setNote(id, text) {
  const el = $(id);
  if (!el) return;
  el.textContent = text;
  el.title = text;
}
// setPill mijenja postojeću pilulu na mjestu, pa joj id i mjesto ostaju isti
const pillIcon = { good: "✓", warn: "△", crit: "✕", off: "○" };
function setPill(el, kind, text) {
  el.className = "st st-" + kind;
  el.textContent = (pillIcon[kind] || "") + " " + text;
}

/* ---------- render ---------- */

function renderSystem(sys) {
  cores = sys.cpu_cores || 1;
  $("hostname").textContent = sys.hostname;
  const kv = $("system-kv");
  kv.replaceChildren();
  const role = sys.role || {};
  let roleTxt = role.routing ? "Router + Firewall" : "Firewall (prosljeđivanje isključeno)";
  roleTxt += ` — ${role.fw_zones || 0} zona, ${role.fw_rules || 0} pravila`;
  if (role.nat_zones && role.nat_zones.length)
    roleTxt += `, NAT na: ${role.nat_zones.join(", ")}`;
  const rows = [
    ["Uloga uređaja", roleTxt],
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

  // statusna traka na dnu stranice
  $("sb-uptime").textContent = fmtUptime(x.uptime_seconds);
  $("sb-load").textContent = x.load.map((n) => n.toFixed(2)).join("  ");
  $("sb-user").textContent = localStorage.getItem("saguaro_user") || "—";
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
  $("sb-state").textContent = h.status === "ok" ? "povezan" : "provjeri vezu";

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

// Čitljivi nazivi umjesto tehničkih (lan, sag_wg0, wan6...)
function ifaceInfo(name) {
  if (name === "lan") return ["LAN — lokalna mreža", "mreža"];
  if (name === "wan") return ["WAN — internet veza", "internet"];
  if (name === "wan6") return ["WAN — internet (IPv6)", "internet"];
  if (name.startsWith("sag_wan"))
    return ["WAN " + name.replace("sag_wan", "") + " — dodatni internet", "internet"];
  if (name === "sag_wg0") return ["WireGuard VPN", "VPN tunel"];
  if (name === "sag_ovpn") return ["OpenVPN", "VPN tunel"];
  if (name.startsWith("sag_vlan"))
    return ["VLAN " + name.replace("sag_vlan", ""), "mreža"];
  return [name, ""];
}
const PROTO_LABEL = {
  static: "statička adresa", dhcp: "automatski (DHCP)",
  dhcpv6: "automatski (IPv6)", wireguard: "WireGuard", none: "tunel",
  pppoe: "PPPoE",
};
function portRoleLabel(role) {
  if (!role) return "slobodan port";
  return ifaceInfo(role)[0].split(" — ")[0];
}

function renderInterfaces(x) {
  // portovi: fizički ethX s ulogom (LAN/WAN/slobodan)
  const ports = $("ports");
  ports.replaceChildren();
  for (const d of x.devices.filter((d) => d.name.startsWith("eth"))) {
    const div = document.createElement("div");
    div.className = "port" + (d.carrier ? " link" : "");
    const name = document.createElement("div");
    name.className = "port-name";
    name.textContent = d.name.replace("eth", "Port ") + " · " + portRoleLabel(d.role);
    const state = d.carrier
      ? stGood("Link " + (d.speed ? d.speed.replace("F", "") + " Mbit" : ""))
      : stOff("Nema kabela");
    const mac = document.createElement("div");
    mac.className = "port-mac";
    mac.textContent = d.name + " · " + d.mac;
    div.append(name, state, mac);
    ports.append(div);
  }

  // logička sučelja: mreže i internet veze prvo, VPN tuneli na kraju
  const order = (i) => {
    const t = ifaceInfo(i.name)[1];
    return t === "mreža" ? 0 : t === "internet" ? 1 : 2;
  };
  const list = [...x.interfaces].sort((a, b) =>
    order(a) - order(b) || a.name.localeCompare(b.name));

  const tb = $("iface-rows");
  tb.replaceChildren();
  for (const i of list) {
    const [label, kind] = ifaceInfo(i.name);
    const tr = document.createElement("tr");

    const tdName = document.createElement("td");
    tdName.textContent = label;
    const code = document.createElement("span");
    code.className = "badge";
    code.textContent = i.name;
    tdName.append(code);
    tr.append(tdName);

    const tdKind = document.createElement("td");
    tdKind.textContent = kind || "—";
    tr.append(tdKind);

    const tdSt = document.createElement("td");
    tdSt.append(i.up ? stGood("Aktivno") : stOff("Neaktivno"));
    tr.append(tdSt);

    const cells = [
      PROTO_LABEL[i.proto] || i.proto,
      kind === "VPN tunel" ? "virtualno" : (i.device || "—"),
      i.ipv4.length ? i.ipv4.join(", ") : "—",
      i.gateway || "—",
      i.dns && i.dns.length ? i.dns.join(", ") : "—",
      i.up ? fmtUptime(i.uptime_seconds) : "—",
    ];
    cells.forEach((c, n) => {
      const td = document.createElement("td");
      td.textContent = c;
      if (n === 5) td.className = "num";
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
    tdAct.append(btnSm("Uredi", false, () => openDeviceDialog(d)));
    if (!d.is_self) {
      tdAct.append(btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati uređaj "${d.hostname}"?`)) return;
        await api("/inventory/devices/" + d.uuid, "DELETE").catch(alertErr);
        loadDevices().catch(onTickError);
      }));
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
    // kvačica je i prikaz i prekidač — jedan stupac umjesto pilule i gumba
    const tdS = document.createElement("td");
    tdS.append(tick(!sv.ignore, async () => {
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
    }, "DHCP pool " + sv.interface));
    tr.append(tdS);
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
  setNote("dhcp-sync-info", info);

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
    tdM.append(tick(!!h.managed, null, "upravlja Saguaro"));
    tr.append(tdM);
    for (const v of [h.customer || "—", h.notes || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openHostDialog(h)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati host "${h.hostname || h.mac}"?`)) return;
        await api("/inventory/hosts/" + h.uuid, "DELETE").catch(alertErr);
        loadDhcp().catch(alertErr);
      }));
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
      tdAct.append(btnSm("U rezervacije", false, () => openHostDialog({
        hostname: l.hostname, mac: l.mac, ipv4: l.ip, managed: true,
      })));
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

let editSpUUID = null;

async function loadDns() {
  const [st, rc, sp] = await Promise.all([
    api("/dns/status"), api("/dns/records"), api("/dns/split")]);

  const spb = $("sp-rows");
  spb.replaceChildren();
  for (const x of sp.split) {
    const tr = document.createElement("tr");
    const tdD = document.createElement("td");
    tdD.textContent = x.domain;
    const sub = document.createElement("span");
    sub.className = "badge";
    sub.textContent = "*." + x.domain;
    tdD.append(sub);
    tr.append(tdD);
    const tdI = document.createElement("td");
    tdI.textContent = x.ip;
    tr.append(tdI);
    const tdE = document.createElement("td");
    tdE.append(tick(!!x.enabled, null));
    tr.append(tdE);
    const tdN = document.createElement("td");
    tdN.textContent = x.notes || "—";
    tr.append(tdN);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openSpDialog(x)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati split DNS za "${x.domain}"?`)) return;
        await api("/dns/split/" + x.uuid, "DELETE").catch(alertErr);
        loadDns().catch(alertErr);
      }));
    tr.append(tdAct);
    spb.append(tr);
  }

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
  setNote("dns-sync-info", info);

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
    tdE.append(tick(!!rec.enabled, null, "DNS zapis"));
    tr.append(tdE);
    const tdN = document.createElement("td");
    tdN.textContent = rec.notes || "—";
    tr.append(tdN);

    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openRecDialog(rec)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati DNS zapis "${rec.name}"?`)) return;
        await api("/dns/records/" + rec.uuid, "DELETE").catch(alertErr);
        loadDns().catch(alertErr);
      }));
    tr.append(tdAct);
    tb.append(tr);
  }
}

function openSpDialog(x) {
  const f = $("sp-form");
  editSpUUID = x ? x.uuid : null;
  $("sp-dialog-title").textContent = editSpUUID
    ? "Uredi split DNS domenu" : "Nova split DNS domena";
  f.elements.domain.value = x ? x.domain : "";
  f.elements.ip.value = x ? x.ip : "";
  f.elements.notes.value = x ? x.notes || "" : "";
  f.elements.enabled.checked = x ? !!x.enabled : true;
  $("sp-dialog").showModal();
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
let editAlUUID = null;

async function loadFirewall() {
  const [st, fw, rl, dmz, n1, al, sn] = await Promise.all([
    api("/firewall/status"), api("/firewall/forwards"), api("/firewall/rules"),
    api("/firewall/dmz"), api("/firewall/nat11"), api("/firewall/aliases"),
    api("/firewall/snat"),
  ]);
  renderSnat(sn);

  const ab = $("al-rows");
  ab.replaceChildren();
  for (const a of al.aliases) {
    const tr = document.createElement("tr");
    const tdN = document.createElement("td");
    tdN.textContent = "@" + a.name;
    tr.append(tdN);
    for (const v of [a.ips, a.notes || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(
      btnSm("Uredi", false, () => openAlDialog(a)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati alias "@${a.name}"? Pravila koja ga koriste ` +
          "past će pri sljedećoj primjeni.")) return;
        await api("/firewall/aliases/" + a.uuid, "DELETE").catch(alertErr);
        loadFirewall().catch(alertErr);
      }));
    tr.append(tdAct);
    ab.append(tr);
  }

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
    tdE.append(tick(!!n.enabled, null));
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
    tr.append(zoneCell(z.name, ""));
    for (const v of [z.input, z.forward, z.masq ? "da" : "ne",
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
  setNote("fw-sync-info", info);
  setNote("pub-sync-info", info);

  const pb = $("pf-rows");
  pb.replaceChildren();
  for (const f of fw.forwards) {
    const tr = document.createElement("tr");
    for (const v of [f.name, f.proto]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    tr.append(zoneCell(f.src_zone, "", ":" + f.src_dport));
    tr.append(zoneCell(f.dest_zone, f.dest_ip +
      (f.dest_port ? ":" + f.dest_port : "")));
    const tdE = document.createElement("td");
    tdE.append(tick(!!f.enabled, null));
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
    for (const v of [f.name, f.proto]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    tr.append(zoneCell(f.src_zone, f.src_ip));
    tr.append(zoneCell(f.dest_zone || "uređaj", f.dest_ip,
      f.dest_port ? ":" + f.dest_port : ""));
    const tdT = document.createElement("td");
    tdT.append(targetMark(f.target));
    tr.append(tdT);
    const tdE = document.createElement("td");
    tdE.append(tick(!!f.enabled, null));
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
  if (!f) {
    d.elements.proto.value = "tcp udp";
    d.elements.target.value = "ACCEPT";
    d.elements.family.value = "any";
  }
  $("rl-dialog").showModal();
}

/* ---------- obrnuti proxy ---------- */

let editRpUUID = null;

async function loadProxy() {
  const x = await api("/proxy");
  loadAcme().catch(() => setPill($("ac-state"), "off", "nedostupno"));
  const tb = $("rp-rows");
  tb.replaceChildren();
  for (const st of x.sites) {
    const tr = document.createElement("tr");
    const tdH = document.createElement("td");
    const b = document.createElement("b");
    b.textContent = st.hostname;
    tdH.append(b);
    tr.append(tdH);
    for (const v of [st.tls_mode === "acme"
      ? "HTTPS · certifikat na uređaju"
      : (st.proto === "http" ? "HTTP" : "HTTPS · prosljeđivanje"),
    `${st.dest_ip}:${st.dest_port}`]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdE = document.createElement("td");
    tdE.append(tick(!!st.enabled, async () => {
      await api("/proxy/sites/" + st.uuid, "PUT",
        { ...st, enabled: !st.enabled }).catch(alertErr);
      loadProxy().catch(alertErr);
    }, st.hostname));
    tr.append(tdE);
    const tdN = document.createElement("td");
    tdN.textContent = st.notes || "—";
    tr.append(tdN);
    const tdA = document.createElement("td");
    tdA.className = "row-actions";
    tdA.append(
      btnSm("Uredi", false, () => openRpDialog(st)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Ukloniti "${st.hostname}" iz objave?`)) return;
        await api("/proxy/sites/" + st.uuid, "DELETE").catch(alertErr);
        loadProxy().catch(alertErr);
      }));
    tr.append(tdA);
    tb.append(tr);
  }

  const active = x.sites.filter((s) => s.enabled).length;
  const badge = $("rp-state");
  if (!x.installed) setPill(badge, "off", "nije instaliran");
  else if (x.running) setPill(badge, "good", "radi");
  else if (active) setPill(badge, "crit", "ne radi");
  else setPill(badge, "off", "nema objavljenih stranica");
  const ips = Object.values(x.wan_ips || {}).flat();
  setNote("rp-note", [
    `${active} aktivnih od ${x.sites.length}`,
    ips.length ? "javne adrese: " + ips.join(", ") : "",
  ].filter(Boolean).join(" · "));

  // preduvjeti
  const list = $("rp-checks");
  list.replaceChildren();
  const items = [
    ["HAProxy instaliran", x.installed ? "spreman" : "paket nedostaje", x.installed],
    ["Portovi", `s interneta 80 i 443 → proxy na ${x.http_port} i ${x.https_port}`, true],
    ["Konfiguracijom upravlja Saguaro",
      x.config_managed ? "da" : "još nije primijenjeno", x.config_managed],
  ];
  for (const [name, detail, ok] of items) {
    const li = document.createElement("li");
    const left = document.createElement("span");
    const what = document.createElement("span");
    what.className = "what";
    what.textContent = name;
    const d = document.createElement("span");
    d.className = "detail";
    d.textContent = detail;
    left.append(what, d);
    li.append(left, ok ? stGood("u redu") : stWarn("treba riješiti"));
    list.append(li);
  }
  $("rp-install").classList.toggle("hidden", x.installed);
  setNote("rp-prereq-note", x.installed ? "sve je spremno" : "treba instalirati HAProxy");
}

function openRpDialog(st) {
  const f = $("rp-form");
  editRpUUID = st ? st.uuid : null;
  $("rp-dialog-title").textContent = editRpUUID ? "Uredi stranicu" : "Nova stranica";
  f.elements.hostname.value = st ? st.hostname : "";
  f.elements.proto.value = st ? st.proto : "https";
  f.elements.dest_ip.value = st ? st.dest_ip : "";
  f.elements.dest_port.value = st ? st.dest_port : "";
  f.elements.tls_mode.value = st ? st.tls_mode : "passthrough";
  f.elements.acme_staging.checked = st ? !!st.acme_staging : false;
  f.elements.notes.value = st ? st.notes || "" : "";
  f.elements.enabled.checked = st ? !!st.enabled : true;
  $("rp-dialog").showModal();
}

/* ---------- certifikati (Let's Encrypt) ---------- */

async function loadAcme() {
  const x = await api("/proxy/acme");
  $("ac-email").value = x.email || "";
  const tb = $("ac-rows");
  tb.replaceChildren();
  const hosts = Object.keys(x.certs || {});
  let issued = 0, soon = 0;
  for (const h of hosts.sort()) {
    const c = x.certs[h];
    if (c.issued) issued++;
    if (c.issued && c.days_left < 20) soon++;
    const tr = document.createElement("tr");
    const t0 = document.createElement("td");
    t0.textContent = h;
    tr.append(t0);
    const t1 = document.createElement("td");
    t1.append(c.issued
      ? (c.days_left < 20 ? stWarn("ističe uskoro") : stGood("izdan"))
      : stOff("nije izdan"));
    tr.append(t1);
    for (const v of [c.issued ? `${c.not_after} (još ${c.days_left} dana)` : "—",
      c.issuer || "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    tb.append(tr);
  }
  const badge = $("ac-state");
  if (!x.installed) setPill(badge, "off", "paket nije instaliran");
  else if (!hosts.length) setPill(badge, "off", "nema stranica s certifikatom na uređaju");
  else if (issued === hosts.length && !soon) setPill(badge, "good", "svi certifikati izdani");
  else if (soon) setPill(badge, "warn", soon + " ističe uskoro");
  else setPill(badge, "warn", `izdano ${issued} od ${hosts.length}`);
  setNote("ac-note", x.email ? "račun: " + x.email : "e-mail računa još nije upisan");
  $("ac-install").classList.toggle("hidden", x.installed);
}

$("ac-save").addEventListener("click", async () => {
  try {
    await api("/proxy/acme", "POST", { email: $("ac-email").value.trim() });
    $("ac-result").textContent = "E-mail spremljen.";
    await loadAcme();
  } catch (e) { $("ac-result").textContent = "Greška: " + (e.message || e); }
});

$("ac-install").addEventListener("click", async () => {
  $("ac-result").textContent = "Instaliram paket acme…";
  try {
    await api("/proxy/acme/install", "POST", {});
    $("ac-result").textContent = "Paket acme je instaliran.";
    await loadAcme();
  } catch (e) { $("ac-result").textContent = "Greška: " + (e.message || e); }
});

$("ac-issue").addEventListener("click", async () => {
  $("ac-result").textContent =
    "Tražim certifikate… (Let's Encrypt sada mora doći na port 80 tih imena)";
  $("ac-log").classList.add("hidden");
  try {
    const r = await api("/proxy/acme/issue", "POST", {});
    const ok = Object.values(r.certs).filter((c) => c.issued).length;
    $("ac-result").textContent =
      `Traženo za ${r.requested.length} imena · izdano ${ok} · povezano u proxy: ${r.linked}.` +
      (ok < r.requested.length ? " Pogledaj ispis ispod — najčešći uzrok je da " +
        "ime ne pokazuje na ovaj uređaj ili port 80 nije dostupan s interneta." : "");
    if (r.log) {
      $("ac-log").textContent = r.log;
      $("ac-log").classList.remove("hidden");
    }
    await loadAcme();
    await loadProxy();
  } catch (e) { $("ac-result").textContent = "Greška: " + (e.message || e); }
});

$("rp-add").addEventListener("click", () => openRpDialog(null));
$("rp-cancel").addEventListener("click", () => $("rp-dialog").close());
$("rp-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    hostname: f.elements.hostname.value.trim(),
    proto: f.elements.proto.value,
    dest_ip: f.elements.dest_ip.value.trim(),
    dest_port: parseInt(f.elements.dest_port.value, 10) || 0,
    tls_mode: f.elements.tls_mode.value,
    acme_staging: f.elements.acme_staging.checked,
    notes: f.elements.notes.value.trim(),
    enabled: f.elements.enabled.checked,
  };
  try {
    if (editRpUUID) await api("/proxy/sites/" + editRpUUID, "PUT", body);
    else await api("/proxy/sites", "POST", body);
    $("rp-dialog").close();
    await loadProxy();
  } catch (e) { alertErr(e); }
});

$("rp-apply").addEventListener("click", async () => {
  $("rp-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/proxy/apply", "POST", {});
    $("rp-result").textContent = r.sites
      ? `Primijenjeno: ${r.sites} stranica, servis ${r.running ? "radi" : "ne radi"}.`
      : "Nema aktivnih stranica — proxy je zaustavljen i portovi zatvoreni.";
    await loadProxy();
  } catch (e) {
    $("rp-result").textContent = "Greška: " + (e.message || e);
  }
});

$("rp-install").addEventListener("click", async () => {
  $("rp-prereq-result").textContent = "Instaliram HAProxy…";
  try {
    await api("/proxy/install", "POST", {});
    $("rp-prereq-result").textContent = "HAProxy je instaliran.";
    await loadProxy();
  } catch (e) {
    $("rp-prereq-result").textContent = "Greška: " + (e.message || e);
  }
});

$("rp-showcfg").addEventListener("click", async () => {
  const pre = $("rp-cfg");
  if (!pre.classList.contains("hidden")) { pre.classList.add("hidden"); return; }
  try {
    const r = await api("/proxy/config");
    pre.textContent = r.generated +
      (r.same === "true" ? "" : "\n\n# (na uređaju je trenutno druga verzija — primijeni)");
    pre.classList.remove("hidden");
  } catch (e) { alertErr(e); }
});

/* ---------- IPv6 ---------- */

const V6_LABEL = { off: "isključen", auto: "automatski", manual: "ručni prefiks" };

async function loadIPv6() {
  const x = await api("/ipv6");
  $("v6-mode").value = x.mode;
  $("v6-prefix").value = x.ula_prefix || "";
  $("v6-prefix-row").classList.toggle("hidden", x.mode !== "manual");

  const badge = $("v6-state");
  if (x.mode === "off") setPill(badge, "off", "isključen");
  else if (x.mode === "auto") setPill(badge,
    x.wan6 && x.wan6.prefix ? "good" : "warn",
    x.wan6 && x.wan6.prefix ? "radi" : "čeka prefiks od pružatelja");
  else setPill(badge, "good", "vlastiti prefiks");
  const parts = [];
  if (x.mode === "auto" && x.wan6) parts.push(x.wan6.prefix
    ? "prefiks od pružatelja: " + x.wan6.prefix
    : "pružatelj još nije dodijelio prefiks");
  if (x.mode === "manual") parts.push("prefiks: " + (x.ula_prefix || "—"));
  parts.push(x.networks.length + " mreža");
  setNote("v6-note", parts.join(" · "));

  const tb = $("v6-rows");
  tb.replaceChildren();
  for (const n of x.networks) {
    const tr = document.createElement("tr");
    for (const v of [n.name, n.ip6assign ? "/" + n.ip6assign : "—"]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    for (const v of [n.ra, n.dhcpv6]) {
      const td = document.createElement("td");
      td.append(v === "server" ? stGood("objavljuje") : stOff(v || "isključeno"));
      tr.append(td);
    }
    const tdA = document.createElement("td");
    tdA.textContent = (n.addresses || []).join(", ") || "—";
    tdA.style.wordBreak = "break-all";
    tr.append(tdA);
    tb.append(tr);
  }
}

$("v6-mode").addEventListener("change", () => {
  $("v6-prefix-row").classList.toggle("hidden", $("v6-mode").value !== "manual");
});

$("v6-save").addEventListener("click", async () => {
  const mode = $("v6-mode").value;
  if (mode !== "off" && !confirm(
    "Uključivanje IPv6\n\n" +
    "Svaki uređaj u mreži dobiva javnu IPv6 adresu — kod IPv6 nema NAT-a.\n" +
    "Dolazni promet ostaje potpuno zabranjen; server se objavljuje izričitim " +
    "pravilom (Firewall rules, obitelj IPv6).\n\nNastaviti?")) return;
  $("v6-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/ipv6", "POST",
      { mode, prefix: $("v6-prefix").value.trim() });
    $("v6-result").textContent =
      `Postavljeno: ${V6_LABEL[r.mode]} · mreža: ${r.networks} · backup: ${r.backup}`;
    await loadIPv6();
  } catch (e) {
    $("v6-result").textContent = "Greška: " + (e.message || e);
  }
});

/* ---------- wireguard ---------- */

let editPeerUUID = null;
// prva slobodna adresa u tunelu — uređaj je izračuna, dijalog je ponudi
let wgNextIP = "";
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
  f.elements.allow_mgmt.checked = !!srv.allow_mgmt;
  wgNextIP = srv.next_tunnel_ip || "";

  // stanje ide u naslovnu traku peerova, kao i kod OpenVPN-a
  const badge = $("wg-state");
  if (!st.installed) setPill(badge, "crit", "paketi nedostaju");
  else if (st.running) setPill(badge, "good", "aktivno");
  else if (srv.configured) setPill(badge, "crit", "neaktivno");
  else setPill(badge, "off", "nije postavljeno");
  $("wg-sum").textContent = [
    (srv.addresses || []).join(", ") || "bez adrese tunela",
    srv.listen_port ? "UDP " + srv.listen_port : "",
  ].filter(Boolean).join(" · ");
  $("wg-pubkey").textContent = srv.public_key || "—";

  const enabledDB = ps.peers.filter((p) => p.enabled).length;
  let info = `U bazi aktivnih peerova: ${enabledDB} · na uređaju: ${st.uci_peers}`;
  if (enabledDB !== st.uci_peers) info += " — ⚠ razlika, potrebna primjena";
  setNote("wg-sync-info", info);

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
    tdE.append(tick(!!p.enabled, async () => {
      await api("/wireguard/peers/" + p.uuid, "PUT", {
        name: p.name, tunnel_ip: p.tunnel_ip, keepalive: p.keepalive,
        enabled: !p.enabled, notes: p.notes,
      }).catch(alertErr);
      loadWireguard().catch(alertErr);
    }, "peer " + p.name));
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
      tdAct.append(
        btnSm("Preuzmi .conf", false, async () => {
          try {
            const c = await api("/wireguard/peers/" + p.uuid + "/config");
            downloadText(c.name + ".conf", c.config);
          } catch (e) { alertErr(e); }
        }),
        btnSm("Prikaži", false, async () => {
          try {
            const c = await api("/wireguard/peers/" + p.uuid + "/config");
            showVpnConfig("WireGuard config — " + c.name + ".conf",
              c.name + ".conf", c.config);
          } catch (e) { alertErr(e); }
        }));
    }
    tdAct.append(
      btnSm("Pristup", false, () => openVpnRulesDialog(p)),
      btnSm("Uredi", false, () => openPeerDialog(p)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati peer "${p.name}"? Njegov ključ se ne može vratiti.`)) return;
        await api("/wireguard/peers/" + p.uuid, "DELETE").catch(alertErr);
        loadWireguard().catch(alertErr);
      }));
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
  // novi peer dobiva ponuđenu prvu slobodnu adresu; može se prepisati
  f.elements.tunnel_ip.value = p ? p.tunnel_ip : wgNextIP;
  $("peer-ip-hint").textContent = p ? ""
    : wgNextIP ? "Ponuđena je prva slobodna adresa u tunelu — potvrdi ili upiši drugu."
      : "Mreža tunela još nije postavljena, pa nema prijedloga adrese.";
  f.elements.public_key.value = p ? p.public_key : "";
  // ključ je identitet peera — kod uređivanja se ne mijenja
  f.elements.public_key.disabled = !!editPeerUUID;
  f.elements.keepalive.value = p && p.keepalive ? p.keepalive : "";
  f.elements.notes.value = p ? p.notes || "" : "";
  f.elements.enabled.checked = p ? !!p.enabled : true;
  $("peer-dialog").showModal();
}

/* ---------- nadzor ---------- */

async function loadMonitorx() {
  const [x, tr] = await Promise.all([api("/monitor"), api("/traffic")]);

  $("nm-unknown").checked = !!x.unknown_alert;
  const tb = $("nm-rows");
  tb.replaceChildren();
  for (const m of x.monitors) {
    const row = document.createElement("tr");
    for (const v of [m.name, m.ip]) {
      const td = document.createElement("td");
      td.textContent = v;
      row.append(td);
    }
    const tdS = document.createElement("td");
    tdS.append(m.last_ok === null ? stOff("Provjerava se")
      : m.last_ok ? stGood("Dostupan") : stCrit("Ne odgovara"));
    row.append(tdS);
    const tdC = document.createElement("td");
    tdC.textContent = m.last_change || "—";
    row.append(tdC);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    tdAct.append(btnSm("Obriši", true, async () => {
      await api("/monitor/" + m.uuid, "DELETE").catch(alertErr);
      loadMonitorx().catch(alertErr);
    }));
    row.append(tdAct);
    tb.append(row);
  }

  const trb = $("tr-rows");
  trb.replaceChildren();
  for (const h of tr.hosts || []) {
    const row = document.createElement("tr");
    for (const v of [h.ip, fmtBytes(h.rx_bytes), fmtBytes(h.tx_bytes),
      String(h.conns)]) {
      const td = document.createElement("td");
      td.textContent = v;
      row.append(td);
    }
    trb.append(row);
  }
  $("tr-hint").textContent = tr.available
    ? "Zbroj od zadnjeg resetiranja brojača (nlbwmon)."
    : "Mjerenje prometa (nlbwmon) nije dostupno.";

  const eb = $("ev-rows");
  eb.replaceChildren();
  for (const e of x.events) {
    const row = document.createElement("tr");
    const tdT = document.createElement("td");
    tdT.textContent = e.ts;
    row.append(tdT);
    const tdL = document.createElement("td");
    tdL.append(e.level === "warning" ? stWarn("Upozorenje") : stGood("Info"));
    row.append(tdL);
    const tdM = document.createElement("td");
    tdM.textContent = e.message;
    row.append(tdM);
    eb.append(row);
  }
}

/* ---------- qos ---------- */

async function loadQos() {
  const x = await api("/qos");
  const tb = $("qos-rows");
  tb.replaceChildren();
  for (const q of x.queues || []) {
    const tr = document.createElement("tr");
    tr.dataset.iface = q.iface;
    const tdN = document.createElement("td");
    tdN.textContent = ifaceInfo(q.iface)[0] + " (" + q.device + ")";
    tr.append(tdN);
    const tdE = document.createElement("td");
    const en = document.createElement("input");
    en.type = "checkbox"; en.className = "q-en"; en.checked = q.enabled;
    tdE.append(en); tr.append(tdE);
    for (const [cls, val] of [["q-down", q.download], ["q-up", q.upload]]) {
      const td = document.createElement("td");
      const inp = document.createElement("input");
      inp.className = cls; inp.style.width = "90px";
      inp.value = val ? (val / 1000).toString() : "";
      td.append(inp); tr.append(td);
    }
    tb.append(tr);
  }
  if (!x.installed)
    $("qos-result").textContent = "Paket sqm-scripts nije instaliran.";
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

  const badge = $("os-state");
  if (!x.enabled) setPill(badge, "off", "isključen");
  else if (x.running) setPill(badge, "good", "radi");
  else setPill(badge, "crit", "ne radi");
  const chosenNames = (x.interfaces || []).map((i) => i.name);
  setNote("os-note", [
    "area " + (x.area || "0"),
    chosenNames.length ? "sučelja: " + chosenNames.join(", ") : "bez odabranih sučelja",
  ].join(" · "));
}

/* ---------- blokade (banIP + adblock-fast) ---------- */

async function loadProtection() {
  const x = await api("/protection");
  fillScan(x.scan);

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

  // postavke i stvarno stanje veze stoje u istom retku — bez druge tablice
  const ifaces = (x.status && x.status.interfaces) || {};
  let online = 0, offline = 0;

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
    const st = ifaces[wn.name] || {};
    if (st.status === "online") online++;
    else if (st.status === "offline") offline++;
    const tdS = document.createElement("td");
    tdS.append(st.status === "online" ? stGood("Radi")
      : st.status === "offline" ? stCrit("Pala")
      : stOff(st.status === "disabled" ? "Isključena" : st.status || "nepoznato"));
    tr.append(tdS);
    const tdP = document.createElement("td");
    tdP.textContent = (st.track_ip || [])
      .map((t) => `${t.ip}: ${t.status === "up" ? t.latency + " ms" : t.status}`)
      .join(" · ") || "—";
    tr.append(tdP);
    tb.append(tr);
  }

  renderMwRules();

  const badge = $("mw-state");
  if (!x.enabled) setPill(badge, "off", "isključen");
  else if (offline) setPill(badge, "crit", offline + " veza ne radi");
  else if (online) setPill(badge, "good", online + " veza radi");
  else setPill(badge, "warn", "bez podataka o vezama");
  setNote("mw-status-hint", x.managed
    ? "" : "Multi-WAN još nije konfiguriran kroz Saguaro — spremi postavke.");
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
// traži li poslužitelj lozinku uz certifikat (određuje je li polje obavezno)
let ovpnPassAuth = false;
// prva slobodna adresa u tunelu — uređaj je izračuna, dijalog je ponudi
let ovpnNextIP = "";
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
  f.elements.allow_mgmt.checked = !!srv.allow_mgmt;
  f.elements.pass_auth.checked = !!srv.pass_auth;
  ovpnPassAuth = !!srv.pass_auth;
  ovpnNextIP = srv.next_tunnel_ip || "";

  // stanje stoji u naslovnoj traci klijenata — bez zasebne ploče za koju se
  // mora skrolati gore
  const badge = $("ov-state");
  const nConn = (st.connected || []).length;
  if (!st.installed) setPill(badge, "crit", "paket nedostaje");
  else if (st.running) setPill(badge, "good", "radi");
  else if (srv.configured) setPill(badge, "crit", "ne radi");
  else setPill(badge, "off", "nije postavljen");
  $("ov-sum").textContent = [
    srv.network || "bez mreže tunela",
    srv.port ? "UDP " + srv.port : "",
    "spojeno " + nConn,
  ].filter(Boolean).join(" · ");

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
    // kad poslužitelj traži lozinku, korisnik bez nje se ne može prijaviti —
    // to mora biti vidljivo na prvi pogled, a ne tek pri neuspjelom spajanju
    const tdP = document.createElement("td");
    if (c.has_pass) tdP.append(stGood("Postavljena"));
    else if (ovpnPassAuth) tdP.append(stCrit("Nedostaje"));
    else tdP.append(stOff("Nema"));
    tr.append(tdP);
    const tdE = document.createElement("td");
    tdE.append(tick(!!c.enabled, async () => {
      await api("/openvpn/clients/" + c.uuid, "PUT", {
        tunnel_ip: c.tunnel_ip, enabled: !c.enabled, notes: c.notes,
      }).catch(alertErr);
      loadOpenvpn().catch(alertErr);
    }, "klijent " + c.name));
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
      btnSm("Preuzmi .ovpn", false, async () => {
        try {
          const x = await api("/openvpn/clients/" + c.uuid + "/config");
          downloadText(x.name + ".ovpn", x.config);
        } catch (e) { alertErr(e); }
      }),
      btnSm("Prikaži", false, async () => {
        try {
          const x = await api("/openvpn/clients/" + c.uuid + "/config");
          showVpnConfig("OpenVPN config — " + x.name + ".ovpn",
            x.name + ".ovpn", x.config);
        } catch (e) { alertErr(e); }
      }),
      ...(c.has_pass ? [btnSm("Ukloni lozinku", true, async () => {
        if (!confirm(`Ukloniti lozinku korisnika "${c.name}"?\n\n` +
          "Ako poslužitelj traži lozinku, taj se korisnik više neće moći " +
          "prijaviti dok mu se ne postavi nova.")) return;
        await api("/openvpn/clients/" + c.uuid, "PUT", {
          tunnel_ip: c.tunnel_ip, enabled: c.enabled, notes: c.notes,
          clear_password: true,
        }).catch(alertErr);
        loadOpenvpn().catch(alertErr);
      })] : []),
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
  // novi klijent dobiva ponuđenu prvu slobodnu adresu; može se prepisati
  f.elements.tunnel_ip.value = c ? c.tunnel_ip : ovpnNextIP;
  $("ovc-ip-hint").textContent = c ? ""
    : ovpnNextIP ? "Ponuđena je prva slobodna adresa u tunelu — potvrdi ili upiši drugu."
      : "Mreža tunela još nije postavljena, pa nema prijedloga adrese.";
  f.elements.notes.value = c ? c.notes || "" : "";
  f.elements.enabled.checked = c ? !!c.enabled : true;
  f.elements.password.value = "";
  // lozinka je obavezna samo kad je provjera uključena i korisnik je još nema
  const need = ovpnPassAuth && !(c && c.has_pass);
  f.elements.password.required = need;
  f.elements.password.placeholder = need
    ? "obavezno — najmanje 8 znakova"
    : (c && c.has_pass ? "prazno = zadrži postojeću" : "najmanje 8 znakova");
  $("ovc-pass-hint").textContent = ovpnPassAuth
    ? "Poslužitelj traži korisničko ime i lozinku uz certifikat. Korisničko " +
      "ime je naziv klijenta; lozinku upiši ovdje."
    : "Lozinka nije obavezna jer poslužitelj traži samo certifikat. Uključi " +
      "traženje lozinke u postavkama poslužitelja za dodatnu zaštitu.";
  $("ovc-dialog").showModal();
}

/* ---------- ažuriranje ---------- */

let upHasStaged = false;
let upHasLatest = false;

async function loadUpdate() {
  // stanje OpenWrt-a se dohvaća usporedo; provjera izdanja ide preko interneta
  // pa ne smije zadržavati prikaz Saguaro dijela
  loadOpenWrt().catch(() => {
    setPill($("ow-state"), "off", "nedostupno");
  });
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
  const [s, sys, mon] = await Promise.all([
    api("/auth/session"), api("/settings/system"), api("/monitor"),
  ]);
  const tz = sys.time || {};
  $("tz-now").textContent = tz.device_time || "—";
  const zsel = $("tz-zone");
  zsel.replaceChildren();
  for (const z of tz.zones || []) {
    const o = document.createElement("option");
    o.value = z; o.textContent = z;
    zsel.append(o);
  }
  if (tz.zonename) zsel.value = tz.zonename;
  $("tz-ntp").checked = !!tz.ntp_server;
  $("tz-servers").value = tz.ntp_servers || "";

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

/* ---------- radnje u tablicama ----------
   Iste oznake u svim tablicama: radnja je ikona (puni naziv ostaje u
   tooltipu), a ispod tablice stoji legenda. Tako red stane u jedan pogled
   umjesto da ga zauzmu četiri gumba s tekstom. */

const ROW_ICONS = {
  "Uredi": "✎",
  "Obriši": "🗑",
  "Ukloni": "🗑",
  "Pristup": "🔑",
  "Ukloni lozinku": "⛔",
  "Preuzmi": "⤓",
  "Preuzmi .ovpn": "⤓",
  "Preuzmi .conf": "⤓",
  "Prikaži": "👁",
  "Vrati": "↺",
  "U rezervacije": "📌",
  "Gore": "▲",
  "Dolje": "▼",
};

function btnSm(label, danger, onclick) {
  const b = document.createElement("button");
  b.className = "btn-sm" + (danger ? " danger" : "");
  const icon = ROW_ICONS[label];
  if (icon) {
    b.classList.add("btn-ico");
    b.textContent = icon;
    b.title = label;
    b.setAttribute("aria-label", label);
  } else {
    b.textContent = label;
  }
  b.onclick = onclick;
  return b;
}

// tick je kvačica stanja: zelena ✔ = uključeno, siva ☐ = isključeno.
// Uz onclick se klikom mijenja stanje (kao u klasičnim firewall sučeljima).
function tick(on, onclick, what) {
  const b = document.createElement("button");
  b.className = "tick" + (on ? " on" : "");
  b.textContent = on ? "✔" : "☐";
  b.title = (on ? "Uključeno" : "Isključeno") +
    (onclick ? (on ? " — klik isključuje" : " — klik uključuje") : "") +
    (what ? " (" + what + ")" : "");
  if (onclick) b.onclick = onclick;
  else b.disabled = true;
  return b;
}

// Zone se boje kao u klasičnim firewall sučeljima: lokalna zelena, internet
// crven, DMZ narančast, gosti plavi, VPN ljubičast. Nepoznata ostaje siva.
const ZONE_CLASS = {
  lan: "z-lan", wan: "z-wan", dmz: "z-dmz", gost: "z-gost", guest: "z-gost",
  vpn: "z-vpn", sagwg: "z-vpn", sagovpn: "z-vpn",
};
function zoneCell(zone, ip, extra) {
  const td = document.createElement("td");
  const z = document.createElement("b");
  z.className = "zone " + (ZONE_CLASS[String(zone).toLowerCase()] || "");
  z.textContent = String(zone).toUpperCase();
  td.append(z);
  const rest = [ip, extra].filter(Boolean).join(" ");
  if (rest) {
    const s = document.createElement("span");
    s.textContent = " " + rest;
    td.append(s);
  }
  return td;
}

// akcija pravila: dopusti/odbij/odbaci — bojom, ne samo riječju
function targetMark(target) {
  const t = String(target || "").toUpperCase();
  const s = document.createElement("b");
  s.className = "zone " + (t === "ACCEPT" ? "z-lan" : t === "DROP" ? "z-wan" : "z-dmz");
  s.textContent = t === "ACCEPT" ? "DOPUSTI" : t === "DROP" ? "ODBACI"
    : t === "REJECT" ? "ODBIJ" : t;
  s.title = t;
  return s;
}

// legenda ispod tablice — objašnjava kvačicu i ikone radnji
function tableLegend(labels, withTick) {
  const p = document.createElement("p");
  p.className = "legend";
  const parts = [];
  if (withTick) parts.push("✔ uključeno (klik isključuje)", "☐ isključeno (klik uključuje)");
  for (const l of labels) parts.push((ROW_ICONS[l] || "") + " " + l.toLowerCase());
  p.textContent = parts.join("   ·   ");
  return p;
}

async function loadBackup() {
  const [x, sch] = await Promise.all([
    api("/backup/archives"), api("/backup/schedule"),
  ]);
  loadOffsite().catch(() => {});
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
  const [x, ws, vl, dd] = await Promise.all([
    api("/network/lan"), api("/network/wans"), api("/network/vlans"),
    api("/ddns")]);
  loadIPv6().catch(() => setPill($("v6-state"), "off", "nedostupno"));

  $("dd-enabled").checked = !!dd.enabled;
  const sel = $("dd-provider");
  sel.replaceChildren();
  for (const p of dd.providers || []) {
    const o = document.createElement("option");
    o.value = p; o.textContent = p;
    sel.append(o);
  }
  if (dd.provider) sel.value = dd.provider;
  $("dd-domain").value = dd.domain || "";
  $("dd-user").value = dd.username || "";
  if (dd.registered_ip)
    $("dd-result").textContent = "Zadnja registrirana adresa: " + dd.registered_ip;

  const vb = $("vlan-rows");
  vb.replaceChildren();
  for (const v of vl.vlans) {
    const tr = document.createElement("tr");
    for (const c of [v.tagged ? "VLAN " + v.vid : "cijeli port", v.name || "—", v.port,
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
      const opis = v.tagged ? `VLAN ${v.vid}` : `mrežu na portu ${v.port}`;
      if (!confirm(`Obrisati ${opis} (${v.name})?\n\nUklanja sučelje, ` +
        "DHCP pool i firewall zonu te mreže.")) return;
      try {
        // mreže na portu imaju interni ID izveden iz porta
        const id = v.tagged ? v.vid : 5000 + parseInt(v.port.replace(/\D/g, ""), 10);
        await api("/network/vlans/" + id, "DELETE");
        $("vlan-result").textContent = opis.charAt(0).toUpperCase() +
          opis.slice(1) + " obrisana.";
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
// Nazivi modula su ustaljeni stručni pojmovi (DHCP, QoS, Port forwarding…) jer
// se tako zovu i u svakoj drugoj opremi — prijevod bi ih učinio neprepoznatljivim.
// Objašnjenje na hrvatskom stoji ispod naslova stranice i u tražilici.
const MODULES = {
  dashboard: ["Dashboard", "Pregled stanja uređaja i mreže", () => null],
  monitorx:  ["Monitoring", "Praćenje uređaja, događaji i potrošnja prometa", () => loadMonitorx()],
  alerts:    ["Alerts", "Što uređaj javlja e-mailom i kome", () => loadAlertsView()],
  audit:     ["Audit log", "Tko je i što promijenio u postavkama uređaja", () => loadAudit()],
  network:   ["Interfaces", "LAN adresa, WAN veze i VLAN mreže", () => loadNetwork()],
  multiwan:  ["Multi-WAN", "Više internet veza — failover, raspodjela i nadzor", () => loadMultiwan()],
  ospf:      ["OSPF", "Dinamičko usmjeravanje — automatska razmjena ruta s routerima", () => loadOspf()],
  qos:       ["QoS", "Ograničenje brzine — glatki pozivi i pravedna raspodjela veze", () => loadQos()],
  dhcp:      ["DHCP", "Dodjela IP adresa i rezervacije za uređaje u mreži", () => loadDhcp()],
  dns:       ["DNS", "Lokalna imena uređaja (npr. nas.lan umjesto IP adrese)", () => loadDns()],
  firewall:  ["Firewall rules", "Zone, pravila prometa i imenovane grupe adresa", () => loadFirewall()],
  publish:   ["Port forwarding / NAT", "Što je iz mreže dostupno s interneta — forwardi, DMZ, 1:1 NAT", () => loadFirewall()],
  hardening: ["System access", "Tko smije do upravljanja i dodatne mjere zaštite", () => loadHardeningView()],
  protection: ["IP blocklists", "Blokada zloćudnih IP adresa s crnih lista (banIP)", () => loadProtection()],
  dnsfilter: ["DNS filter", "Blokada reklamnih i zloćudnih domena (adblock)", () => loadProtection()],
  scan:      ["Scan detection", "Prepoznavanje skeniranja portova i privremena blokada izvora", () => loadProtection()],
  rproxy:    ["Reverse proxy", "Više web servisa iza jedne javne adrese, razdvojenih po imenu", () => loadProxy()],
  wireguard: ["WireGuard", "Udaljeni pristup — moderni VPN s ključevima", () => loadWireguard()],
  openvpn:   ["OpenVPN", "Udaljeni pristup — klasični VPN s certifikatima", () => loadOpenvpn()],
  devices:   ["Inventory", "Inventar opreme — ovaj uređaj i susjedni", () => loadDevices()],
  backup:    ["Backup", "Sigurnosne kopije uređaja i vraćanje", () => loadBackup()],
  update:    ["Updates", "Nadogradnja Saguara i sustava uređaja (OpenWrt)", () => loadUpdate()],
  settings:  ["Settings", "Lozinke, sesije, vrijeme i API token", () => loadSettings()],
  logs:      ["System log", "Sustavski log, trajno spremanje i slanje na poslužitelj", () => loadLogsView()],
  help:      ["Help", "Upute za rad — kako koristiti svaki modul", () => null],
};
// Hrvatski pojmovi za tražilicu, da "vatrozid" nađe Firewall rules.
const MODULE_KEYS = {
  dashboard: "nadzorna ploča pregled stanje",
  monitorx: "nadzor praćenje ping promet potrošnja",
  alerts: "upozorenja obavijesti e-mail mail dojava",
  audit: "promjene tko je mijenjao dnevnik izmjena",
  network: "mreža sučelja lan wan vlan adresa",
  multiwan: "više veza failover pričuvna veza rezervna",
  ospf: "usmjeravanje rute routing",
  qos: "brzina ograničenje prioritet promet",
  dhcp: "dodjela adresa rezervacije zakup lease",
  dns: "imena domene razlučivanje",
  firewall: "vatrozid pravila zone promet blokiraj dopusti",
  publish: "objava servera prosljeđivanje portova dmz nat",
  hardening: "očvršćivanje hardening pristup upravljanju sigurnost ssh acl",
  protection: "blokade crne liste zloćudne adrese banip zemlje",
  dnsfilter: "blokada reklama domene adblock oglasi",
  scan: "skeniranje portova napad izviđanje detekcija",
  rproxy: "obrnuti proxy reverse haproxy objava web servisa ime domena sni",
  wireguard: "vpn udaljeni pristup ključevi",
  openvpn: "vpn udaljeni pristup certifikati ovpn",
  devices: "uređaji inventar oprema",
  backup: "sigurnosna kopija vraćanje arhiva",
  update: "ažuriranje nadogradnja verzija",
  settings: "postavke lozinka sesija vrijeme token",
  logs: "logovi zapisi dnevnik syslog",
  help: "pomoć upute priručnik",
};
// Skupine su razdvojene po poslu: filtriranje prometa po adresama i domenama
// više nije u istoj skupini kao pravila vatrozida.
const NAV_GROUPS = [
  ["Status", ["dashboard", "monitorx", "alerts", "audit"]],
  ["Network", ["network", "multiwan", "ospf", "qos", "dhcp", "dns"]],
  ["Firewall", ["firewall", "publish", "hardening"]],
  ["Filtering", ["protection", "dnsfilter", "scan"]],
  ["Proxy", ["rproxy"]],
  ["VPN", ["wireguard", "openvpn"]],
  ["System", ["settings", "logs", "backup", "devices", "update", "help"]],
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

  $("side-h").textContent = NAV_GROUPS[gi][0];
  const sub = $("subnav");
  sub.replaceChildren();
  for (const id of NAV_GROUPS[gi][1]) {
    const b = document.createElement("button");
    b.className = "subtab" + (id === active ? " active" : "");
    b.textContent = MODULES[id][0];
    b.title = MODULES[id][1];
    b.onclick = () => { location.hash = "#/" + id; };
    sub.append(b);
  }
}

/* ---------- ploče: naslovna traka i sklapanje ----------
   Kartice u HTML-u ostaju iste; ovdje im se doda gumb za sklapanje, a sadržaj
   se omota u .panel-body da se može sakriti. Stanje se pamti po modulu i
   naslovu ploče, pa ostaje i nakon osvježavanja stranice. */

const PANEL_KEY = "sag.panels";
let panelState = {};
try { panelState = JSON.parse(localStorage.getItem(PANEL_KEY) || "{}"); } catch (e) { panelState = {}; }

function savePanels() {
  try { localStorage.setItem(PANEL_KEY, JSON.stringify(panelState)); } catch (e) { /* privatni način */ }
}

function upgradePanels() {
  for (const card of document.querySelectorAll(".card")) {
    if (card.dataset.panel) continue;
    card.dataset.panel = "1";
    let head = card.firstElementChild;
    while (head && head.nodeType === 3) head = head.nextElementSibling;
    // kartica bez naslova — samo dobije unutarnji razmak
    if (!head || (head.tagName !== "H2" && !head.classList.contains("card-head"))) {
      card.classList.add("plain");
      continue;
    }
    if (head.tagName === "H2") {                 // goli naslov → naslovna traka
      const bar = document.createElement("div");
      bar.className = "card-head";
      card.insertBefore(bar, head);
      bar.append(head);
      head = bar;
    } else {                                     // postojeći alati desno od naslova
      // pilule stanja i kratki sažetak ostaju uz naslov, gumbi idu desno
      const tools = document.createElement("div");
      tools.className = "head-tools";
      const h2 = head.querySelector("h2");
      let el = h2 && h2.nextElementSibling;
      while (el) {
        const next = el.nextElementSibling;
        if (!el.classList.contains("st") && !el.classList.contains("head-note"))
          tools.append(el);
        el = next;
      }
      if (tools.childElementCount) head.append(tools);
    }
    const body = document.createElement("div");
    body.className = "panel-body";
    while (head.nextSibling) body.append(head.nextSibling);
    card.append(body);

    const view = card.closest(".view");
    const title = (head.querySelector("h2") || {}).textContent || "";
    const key = (view ? view.id : "?") + "|" + title.trim();
    const chev = document.createElement("button");
    chev.type = "button";
    chev.className = "chev";
    const paint = () => {
      const closed = !!panelState[key];
      body.classList.toggle("hidden", closed);
      chev.textContent = closed ? "▸" : "▾";
      chev.title = closed ? "Rasklopi" : "Sklopi";
    };
    const toggle = () => { panelState[key] = !panelState[key]; savePanels(); paint(); };
    chev.onclick = toggle;
    const h2 = head.querySelector("h2");
    if (h2) h2.onclick = toggle;
    head.prepend(chev);
    paint();
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
    // safe mode: uspješan dohvat sučelja = potvrda da promjena nije zaključala
    api("/rollback/confirm", "POST", {}).then((r) => {
      if (r.confirmed)
        $("refreshed").textContent = "Promjena '" + r.reason + "' potvrđena (safe mode).";
    }).catch(() => {});
  } catch (e) {
    if (e && e.unauthorized) { logout(true); return; }
    throw e;
  }
  $("login").classList.add("hidden");
  $("app").classList.remove("hidden");
  upgradePanels();
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
  $("firstpw-form").classList.add("hidden");
  $("login-form").classList.remove("hidden");
  firstPassCurrent = "";
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
    localStorage.setItem("saguaro_user", $("user-input").value.trim());
    const firstPass = $("pass-input").value;
    $("pass-input").value = "";
    // zadana lozinka s instalacije: uređaj do promjene ne dopušta ništa drugo
    if (data.must_change_password) {
      showFirstPasswordForm(firstPass);
      return;
    }
    await start();
  } catch {
    logout(true);
  }
});

/* ---------- obavezna promjena zadane lozinke ---------- */

let firstPassCurrent = "";

function showFirstPasswordForm(current) {
  firstPassCurrent = current;
  $("login-form").classList.add("hidden");
  $("firstpw-error").classList.add("hidden");
  $("firstpw-form").classList.remove("hidden");
  $("firstpw-new").focus();
}

$("firstpw-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const err = $("firstpw-error");
  const pw = $("firstpw-new").value;
  if (pw !== $("firstpw-rep").value) {
    err.textContent = "Lozinke se ne podudaraju.";
    err.classList.remove("hidden");
    return;
  }
  try {
    await api("/auth/password", "POST", { current: firstPassCurrent, new: pw });
  } catch (e) {
    err.textContent = (e && e.message) || "Promjena lozinke nije uspjela.";
    err.classList.remove("hidden");
    return;
  }
  firstPassCurrent = "";
  $("firstpw-new").value = "";
  $("firstpw-rep").value = "";
  $("firstpw-form").classList.add("hidden");
  $("login-form").classList.remove("hidden");
  await start();
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

$("sp-add").addEventListener("click", () => openSpDialog(null));
$("sp-cancel").addEventListener("click", () => $("sp-dialog").close());
$("sp-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    domain: f.elements.domain.value.trim(),
    ip: f.elements.ip.value.trim(),
    notes: f.elements.notes.value.trim(),
    enabled: f.elements.enabled.checked,
  };
  try {
    if (editSpUUID) await api("/dns/split/" + editSpUUID, "PUT", body);
    else await api("/dns/split", "POST", body);
    $("sp-dialog").close();
    await loadDns();
  } catch (e) { alertErr(e); }
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
      `Primijenjeno: ${r.applied} zapisa i ${r.applied_split || 0} split DNS ` +
      `domena (uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    await loadDns();
  } catch (e) {
    $("dns-apply-result").textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
});

function openAlDialog(a) {
  const f = $("al-form");
  editAlUUID = a ? a.uuid : null;
  $("al-dialog-title").textContent = editAlUUID ? "Uredi alias" : "Novi alias";
  f.elements.name.value = a ? a.name : "";
  f.elements.ips.value = a ? a.ips : "";
  f.elements.notes.value = a ? a.notes || "" : "";
  $("al-dialog").showModal();
}
$("al-add").addEventListener("click", () => openAlDialog(null));
$("al-cancel").addEventListener("click", () => $("al-dialog").close());
$("al-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    name: f.elements.name.value.trim(),
    ips: f.elements.ips.value.trim(),
    notes: f.elements.notes.value.trim(),
  };
  try {
    if (editAlUUID) await api("/firewall/aliases/" + editAlUUID, "PUT", body);
    else await api("/firewall/aliases", "POST", body);
    $("al-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

$("sy-refresh").addEventListener("click", () => refreshSyslog());

$("nm-add").addEventListener("click", () => {
  $("nm-form").reset();
  $("nm-dialog").showModal();
});
$("nm-cancel").addEventListener("click", () => $("nm-dialog").close());
$("nm-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  try {
    await api("/monitor", "POST", {
      name: f.elements.name.value.trim(),
      ip: f.elements.ip.value.trim(),
    });
    $("nm-dialog").close();
    await loadMonitorx();
  } catch (e) { alertErr(e); }
});
$("nm-unknown").addEventListener("change", async () => {
  try {
    const r = await api("/monitor/settings", "POST",
      { unknown_alert: $("nm-unknown").checked });
    $("nm-result").textContent = r.unknown_alert
      ? "Alarm uključen — postojeći uređaji upisani su kao poznati."
      : "Alarm isključen.";
  } catch (e) { alertErr(e); }
});

$("qos-save").addEventListener("click", async () => {
  const queues = [];
  for (const tr of $("qos-rows").children) {
    queues.push({
      iface: tr.dataset.iface,
      enabled: tr.querySelector(".q-en").checked,
      download: Math.round(parseFloat(tr.querySelector(".q-down").value) * 1000) || 0,
      upload: Math.round(parseFloat(tr.querySelector(".q-up").value) * 1000) || 0,
    });
  }
  $("qos-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/qos", "POST", { queues });
    $("qos-result").textContent = "Primijenjeno. Backup: " + r.backup;
  } catch (e) {
    $("qos-result").textContent = "Greška: " + (e.message || e);
  }
});

$("dd-save").addEventListener("click", async () => {
  $("dd-result").textContent = "Spremam…";
  try {
    const r = await api("/ddns", "POST", {
      enabled: $("dd-enabled").checked,
      provider: $("dd-provider").value,
      domain: $("dd-domain").value.trim(),
      username: $("dd-user").value.trim(),
      password: $("dd-pass").value,
    });
    $("dd-result").textContent = r.enabled
      ? "DDNS uključen — prva registracija slijedi za koju minutu."
      : "DDNS isključen.";
    $("dd-pass").value = "";
  } catch (e) {
    $("dd-result").textContent = "Greška: " + (e.message || e);
  }
});

$("tz-save").addEventListener("click", async () => {
  $("tz-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/settings/time", "POST", {
      zonename: $("tz-zone").value,
      ntp_server: $("tz-ntp").checked,
      ntp_servers: $("tz-servers").value.trim(),
    });
    $("tz-result").textContent = "Zona postavljena: " + r.zonename +
      ". Novi zapisi u logu koriste lokalno vrijeme.";
    setTimeout(() => loadSettings().catch(() => {}), 1500);
  } catch (e) {
    $("tz-result").textContent = "Greška: " + (e.message || e);
  }
});

$("acl-save").addEventListener("click", async () => {
  const en = $("acl-enabled").checked;
  if (en && !confirm("Uključiti ograničenje pristupa?\n\nAko trenutna adresa " +
    "tvog računala nije na popisu, izgubit ćeš pristup — safe mode će " +
    "promjenu vratiti za 2 minute.")) return;
  $("acl-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/settings/mgmtacl", "POST",
      { enabled: en, allow: $("acl-allow").value.trim() });
    if (r.safe_mode) {
      // još imamo pristup — odmah potvrdi da se promjena ne vrati
      const c = await api("/rollback/confirm", "POST", {});
      $("acl-result").textContent = "Ograničenje aktivno i potvrđeno (pristup radi).";
    } else {
      $("acl-result").textContent = "Ograničenje isključeno.";
    }
  } catch (e) {
    $("acl-result").textContent = "Greška: " + (e.message || e) +
      " — ako si izgubio pristup, pričekaj 2 minute (safe mode).";
  }
});

$("sm-save").addEventListener("click", async () => {
  try {
    await api("/settings/smtp", "POST", {
      enabled: $("sm-enabled").checked,
      host: $("sm-host").value.trim(),
      port: parseInt($("sm-port").value, 10) || 587,
      user: $("sm-user").value.trim(),
      pass: $("sm-pass").value,
      from: $("sm-from").value.trim(),
      to: $("sm-to").value.trim(),
    });
    $("sm-result").textContent = "Spremljeno.";
    $("sm-pass").value = "";
  } catch (e) {
    $("sm-result").textContent = "Greška: " + (e.message || e);
  }
});
$("sm-test").addEventListener("click", async () => {
  $("sm-result").textContent = "Šaljem probnu poruku…";
  try {
    await api("/notify/test", "POST", {});
    $("sm-result").textContent = "Probna poruka poslana — provjeri sandučić.";
  } catch (e) {
    $("sm-result").textContent = "Greška: " + (e.message || e);
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
  const allowInternal = f.elements.allow_internal.checked;
  try {
    for (const j of jobs) {
      await api("/firewall/forwards", "POST", {
        ...j, dest_ip: ip, src_dip: srcDip, reflection: refl,
        notes: "čarobnjak: objava servera " + prefix,
      });
      // pristup iznutra: promet iz bilo koje interne mreže prema serveru
      if (allowInternal) {
        await api("/firewall/rules", "POST", {
          name: j.name + "-interno", proto: j.proto,
          src_zone: "*", dest_zone: "*", dest_ip: ip, dest_port: j.src_dport,
          target: "ACCEPT",
          notes: "čarobnjak: pristup serveru " + prefix + " iz internih mreža",
        }).catch(() => {});
      }
    }
    $("pub-dialog").close();
    $("fw-apply-result").textContent =
      `Čarobnjak je stvorio ${jobs.length} forwarda` +
      (allowInternal ? " i pripadna pravila pristupa" : "") +
      ` — klikni "Primijeni firewall".`;
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
  for (const n of ["name", "family", "proto", "src_zone", "src_ip", "dest_zone",
    "dest_ip", "dest_port", "target", "start_time", "stop_time", "weekdays",
    "notes"]) body[n] = f.elements[n].value.trim();
  body.enabled = f.elements.enabled.checked;
  try {
    if (editRlUUID) await api("/firewall/rules/" + editRlUUID, "PUT", body);
    else await api("/firewall/rules", "POST", body);
    $("rl-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

// Firewall i Objava servera dijele istu primjenu (jedan endpoint primjenjuje
// forwarde, pravila i 1:1 NAT), pa oba gumba rade isti posao.
async function applyFirewall(btnId, outId) {
  const btn = $(btnId);
  btn.disabled = true;
  $(outId).textContent = "Primjenjujem…";
  try {
    const r = await api("/firewall/apply", "POST", {});
    $(outId).textContent =
      `Primijenjeno: ${r.applied_forwards} forwarda + ${r.applied_rules} pravila ` +
      `(uklonjeno starih: ${r.removed}). Backup: ${r.backup}`;
    await loadFirewall();
  } catch (e) {
    $(outId).textContent = "Greška: " + (e.message || e);
  } finally {
    btn.disabled = false;
  }
}
$("fw-apply").addEventListener("click", () => applyFirewall("fw-apply", "fw-apply-result"));
$("pub-apply").addEventListener("click", () => applyFirewall("pub-apply", "pub-apply-result"));

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
/* ---------- izlazne adrese (SNAT) ---------- */

// Zadnji odgovor uređaja — treba za padajući popis mreža i javnih adresa
// u dijalogu, da se ne prepisuju ručno.
let snatData = { snat: [], wan_ips: {}, networks: {} };
let editSnUUID = null;

function renderSnat(x) {
  snatData = x;
  const tb = $("sn-rows");
  tb.replaceChildren();
  const list = x.snat || [];
  x.snat.forEach((n, i) => {
    const tr = document.createElement("tr");
    const tdI = document.createElement("td");
    tdI.textContent = String(i + 1);
    tr.append(tdI);
    for (const v of [n.name, n.src_ip, n.dest_ip || "sva",
      n.proto === "all" ? "svi" : n.proto]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const tdA = document.createElement("td");
    const b = document.createElement("b");
    b.className = "zone z-wan";
    b.textContent = n.snat_ip;
    tdA.append(b);
    const z = document.createElement("span");
    z.textContent = " (" + n.out_zone + ")";
    tdA.append(z);
    tr.append(tdA);
    const tdE = document.createElement("td");
    tdE.append(tick(!!n.enabled, async () => {
      await api("/firewall/snat/" + n.uuid, "PUT",
        { ...n, enabled: !n.enabled }).catch(alertErr);
      loadFirewall().catch(alertErr);
    }, n.name));
    tr.append(tdE);
    const tdAct = document.createElement("td");
    tdAct.className = "row-actions";
    const move = async (dir) => {
      await api("/firewall/snat/" + n.uuid + "/move", "POST", { dir }).catch(alertErr);
      loadFirewall().catch(alertErr);
    };
    const up = btnSm("Gore", false, () => move("up"));
    up.disabled = i === 0;
    const down = btnSm("Dolje", false, () => move("down"));
    down.disabled = i === list.length - 1;
    tdAct.append(up, down,
      btnSm("Uredi", false, () => openSnDialog(n)),
      btnSm("Obriši", true, async () => {
        if (!confirm(`Obrisati pravilo "${n.name}"?`)) return;
        await api("/firewall/snat/" + n.uuid, "DELETE").catch(alertErr);
        loadFirewall().catch(alertErr);
      }));
    tr.append(tdAct);
    tb.append(tr);
  });
  const nAddr = Object.values(x.wan_ips || {}).reduce((a, v) => a + v.length, 0);
  setNote("sn-note", list.length
    ? `${list.length} pravila · javnih adresa na WAN-u: ${nAddr}`
    : `nema pravila — sve izlazi sa zadanom adresom · javnih adresa na WAN-u: ${nAddr}`);
}

function openSnDialog(n) {
  const f = $("sn-form");
  editSnUUID = n ? n.uuid : null;
  $("sn-dialog-title").textContent = editSnUUID ? "Uredi izlaznu adresu" : "Nova izlazna adresa";
  f.elements.name.value = n ? n.name : "";
  f.elements.out_zone.value = n ? n.out_zone : "wan";
  f.elements.src_ip.value = n ? n.src_ip : "";
  f.elements.snat_ip.value = n ? n.snat_ip : "";
  f.elements.proto.value = n ? n.proto : "all";
  f.elements.dest_ip.value = n ? n.dest_ip || "" : "";
  f.elements.dest_port.value = n ? n.dest_port || "" : "";
  f.elements.notes.value = n ? n.notes || "" : "";
  f.elements.enabled.checked = n ? !!n.enabled : true;

  // ponuda stvarnih javnih adresa s uređaja
  const dl = $("sn-wanips");
  dl.replaceChildren();
  for (const [iface, addrs] of Object.entries(snatData.wan_ips || {})) {
    for (const a of addrs) {
      const o = document.createElement("option");
      o.value = a;
      o.label = iface;
      dl.append(o);
    }
  }
  // ponuda lokalnih mreža — odabir upiše CIDR u polje izvora
  const sel = $("sn-net");
  sel.replaceChildren();
  const first = document.createElement("option");
  first.value = "";
  first.textContent = "— odaberi pa se upiše gore —";
  sel.append(first);
  for (const [name, cidr] of Object.entries(snatData.networks || {})) {
    const o = document.createElement("option");
    o.value = cidr;
    o.textContent = `${name} (${cidr})`;
    sel.append(o);
  }
  sel.onchange = () => { if (sel.value) f.elements.src_ip.value = sel.value; };
  $("sn-dialog").showModal();
}

$("sn-add").addEventListener("click", () => openSnDialog(null));
$("sn-cancel").addEventListener("click", () => $("sn-dialog").close());
$("sn-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {};
  for (const n of ["name", "out_zone", "src_ip", "snat_ip", "proto",
    "dest_ip", "dest_port", "notes"])
    body[n] = f.elements[n].value.trim();
  body.enabled = f.elements.enabled.checked;
  try {
    if (editSnUUID) await api("/firewall/snat/" + editSnUUID, "PUT", body);
    else await api("/firewall/snat", "POST", body);
    $("sn-dialog").close();
    await loadFirewall();
  } catch (e) { alertErr(e); }
});

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
  vlanKindFields();
  $("vlan-dialog").showModal();
});
function vlanKindFields() {
  const tagged = $("vlan-kind").value === "tagged";
  $("vlan-vid-wrap").classList.toggle("hidden", !tagged);
  $("vlan-form").elements.vid.required = tagged;
  $("vlan-hint-tagged").classList.toggle("hidden", !tagged);
  $("vlan-hint-port").classList.toggle("hidden", tagged);
}
$("vlan-kind").addEventListener("change", vlanKindFields);
$("vlan-cancel").addEventListener("click", () => $("vlan-dialog").close());
$("vlan-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const tagged = $("vlan-kind").value === "tagged";
  const body = {
    vid: tagged ? (parseInt(f.elements.vid.value, 10) || 0) : 0,
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
    $("vlan-result").textContent = (r.tagged
      ? `Stvoren VLAN na ${r.device}.` : `Mreža na portu ${r.device} stvorena.`) +
      ` Backupi: ${r.backups.join(", ")}`;
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
    allow_mgmt: f.elements.allow_mgmt.checked,
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

// Config se ne kopira samo u međuspremnik nego se i preuzima kao datoteka —
// VPN aplikacije (WireGuard, OpenVPN Connect) uvoze upravo datoteku, pa je
// kopiranje teksta korak previše.
function showVpnConfig(title, filename, text) {
  $("wgconf-title").textContent = title;
  $("wgconf-text").value = text;
  $("wgconf-dialog").dataset.filename = filename;
  $("wgconf-dialog").showModal();
}

function downloadText(filename, text) {
  const blob = new Blob([text], { type: "application/octet-stream" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

$("wgconf-download").addEventListener("click", () => {
  const d = $("wgconf-dialog");
  downloadText(d.dataset.filename || "saguaro-vpn.conf", $("wgconf-text").value);
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
    allow_mgmt: f.elements.allow_mgmt.checked,
    pass_auth: f.elements.pass_auth.checked,
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
    password: f.elements.password.value,
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

/* ---------- nadogradnja OpenWrt-a ---------- */

// Zadnji odgovor uređaja o stanju sustava; drži i podatke o pripremljenoj
// slici, pa gumbi znaju smije li se ići dalje.
let owStatus = {};

async function loadOpenWrt() {
  const x = await api("/openwrt/status");
  owStatus = x;
  const kv = $("ow-kv");
  kv.replaceChildren();
  const rows = [
    ["Instalirano", `OpenWrt ${x.version} (${x.revision})`],
    ["Platforma", `${x.target} · ${x.rootfs} · pokretanje ${x.boot_mode}`],
    ["Paketa na uređaju", String(x.packages)],
    ["Zadnji backup", x.last_backup
      ? `${x.last_backup} (prije ${x.last_backup_age_min} min)` : "nema"],
  ];
  if (x.candidate) rows.splice(1, 0, ["Dostupno izdanje", x.candidate]);
  if (x.staged) {
    rows.push(["Pripremljena slika",
      `${x.staged.image || "(vlastita)"} · ${fmtBytes(x.staged.size)}`]);
  }
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }

  const badge = $("ow-state");
  if (x.latest_error) setPill(badge, "warn", "nema pristupa servisu");
  else if (x.candidate) setPill(badge, "warn", "dostupno " + x.candidate);
  else setPill(badge, "good", "najnovije u grani");
  setNote("ow-note", x.latest_error || (x.latest
    ? "grane: " + x.latest.join(", ") : ""));

  $("ow-confirm").placeholder = x.hostname || "ime uređaja";
  $("ow-fetch").disabled = !owBuilt;
  $("ow-flash").disabled = !x.staged;

  // provjera paketa nakon nadogradnje
  try {
    const p = await api("/openwrt/packages");
    if (p.checked && p.missing && p.missing.length) {
      $("ow-pkg-note").textContent =
        `Nakon nadogradnje nedostaje ${p.missing.length} paketa: ` +
        p.missing.slice(0, 12).join(", ") + (p.missing.length > 12 ? "…" : "");
      $("ow-pkg-actions").classList.remove("hidden");
    } else {
      $("ow-pkg-note").textContent = p.checked
        ? "Svi paketi s popisa prije nadogradnje su na uređaju." : "";
      $("ow-pkg-actions").classList.add("hidden");
    }
  } catch { /* popis nije obavezan */ }
}

let owBuilt = null; // {url, sha256, image, version}

$("ow-refresh").addEventListener("click", () => loadOpenWrt().catch(alertErr));

$("ow-build").addEventListener("click", async () => {
  $("ow-build-result").textContent =
    "Naručujem sliku s popisom paketa ovog uređaja… (prvi put zna trajati par minuta)";
  try {
    let r = await api("/openwrt/build", "POST", {});
    // servis gradi u pozadini — pitaj ponovno dok ne bude gotovo
    for (let i = 0; r.state === "building" && i < 30; i++) {
      $("ow-build-result").textContent =
        `Servis gradi sliku (${r.status || "u tijeku"})… pokušaj ${i + 1}/30`;
      await new Promise((res) => setTimeout(res, 10000));
      r = await api("/openwrt/build", "POST", {});
    }
    if (r.state !== "ready") {
      $("ow-build-result").textContent =
        "Slika još nije gotova — pokušaj ponovno za koju minutu.";
      return;
    }
    owBuilt = r;
    $("ow-fetch").disabled = false;
    $("ow-build-result").textContent =
      `Slika je spremna: ${r.image} (otisak ${r.sha256.slice(0, 16)}…). ` +
      "Sljedeći korak: preuzmi je na uređaj.";
  } catch (e) {
    $("ow-build-result").textContent = "Greška: " + (e.message || e);
  }
});

$("ow-fetch").addEventListener("click", async () => {
  if (!owBuilt) return;
  $("ow-build-result").textContent = "Preuzimam sliku na uređaj…";
  try {
    const r = await api("/openwrt/fetch", "POST", owBuilt);
    $("ow-build-result").textContent =
      `Slika je na uređaju (${fmtBytes(r.size_bytes)}), otisak provjeren.`;
    await loadOpenWrt();
  } catch (e) {
    $("ow-build-result").textContent = "Greška: " + (e.message || e);
  }
});

$("ow-upload").addEventListener("click", async () => {
  const f = $("ow-file").files[0];
  if (!f) { alert("Odaberi .img.gz sliku."); return; }
  $("ow-build-result").textContent = "Učitavam sliku…";
  try {
    const r = await fetch(API + "/openwrt/upload", {
      method: "POST",
      headers: { Authorization: "Bearer " + token, "X-Filename": f.name },
      body: f,
    });
    const data = await r.json().catch(() => ({}));
    if (r.status === 401) throw { unauthorized: true };
    if (!r.ok) throw new Error(data.error || "HTTP " + r.status);
    $("ow-build-result").textContent =
      `Slika učitana (${fmtBytes(data.size_bytes)}), otisak ${data.sha256.slice(0, 16)}…`;
    $("ow-file").value = "";
    await loadOpenWrt();
  } catch (e) {
    if (e && e.unauthorized) { logout(true); return; }
    $("ow-build-result").textContent = "Greška: " + (e.message || e);
  }
});

$("ow-flash").addEventListener("click", async () => {
  const name = $("ow-confirm").value.trim();
  if (!confirm(
    "Nadogradnja sustava uređaja\n\n" +
    "Uređaj se ponovno pokreće i 1–3 minute nije dostupan.\n" +
    "Ako slika ne odgovara uređaju, za oporavak treba fizički pristup.\n\n" +
    "Nastaviti?")) return;
  $("ow-flash-result").textContent = "Radim backup i pokrećem nadogradnju…";
  try {
    const r = await api("/openwrt/flash", "POST", { confirm: name });
    $("ow-flash-result").textContent =
      `Nadogradnja pokrenuta (backup ${r.backup}). ${r.note}`;
    stopTimers();
    owWaitForDevice();
  } catch (e) {
    $("ow-flash-result").textContent = "Greška: " + (e.message || e);
  }
});

// owWaitForDevice čeka da se uređaj digne i sam osvježi sučelje
function owWaitForDevice() {
  let n = 0;
  const tick = async () => {
    n++;
    $("ow-flash-result").textContent =
      `Uređaj se nadograđuje i ponovno pokreće… (${n * 10} s)`;
    try {
      const r = await fetch(API + "/health", { cache: "no-store" });
      if (r.ok) {
        $("ow-flash-result").textContent =
          "Uređaj je opet dostupan. Osvježavam sučelje…";
        setTimeout(() => location.reload(), 2000);
        return;
      }
    } catch { /* još se diže */ }
    if (n * 10 > 600) {
      $("ow-flash-result").textContent =
        "Uređaj se ne javlja ni nakon 10 minuta. Provjeri ima li struju i vezu; " +
        "ako se ne digne, potreban je fizički pristup (slika se vraća s USB-a).";
      return;
    }
    setTimeout(tick, 10000);
  };
  setTimeout(tick, 20000);
}

$("ow-pkg-restore").addEventListener("click", async () => {
  $("ow-pkg-note").textContent = "Doinstaliram…";
  try {
    const r = await api("/openwrt/packages/restore", "POST", {});
    $("ow-pkg-note").textContent = r.installed.length
      ? "Doinstalirano: " + r.installed.join(", ")
      : "Nema paketa za doinstalaciju.";
    await loadOpenWrt();
  } catch (e) {
    $("ow-pkg-note").textContent = "Greška: " + (e.message || e);
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

$("devpw-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  if (f.elements.new1.value !== f.elements.new2.value) {
    $("devpw-result").textContent = "Nove lozinke se ne podudaraju.";
    return;
  }
  $("devpw-result").textContent = "Mijenjam…";
  try {
    const r = await api("/system/device-password", "POST", {
      new: f.elements.new1.value,
      confirm: f.elements.new2.value,
    });
    f.reset();
    $("devpw-result").textContent =
      "Lozinka uređaja promijenjena. Kopija prethodnog stanja: " + r.backup;
  } catch (e) {
    $("devpw-result").textContent = "Greška: " + (e.message || e);
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

/* ---------- upozorenja (Nadzor) ---------- */

async function loadAlerts() {
  const a = await api("/alerts");
  const box = $("al-kinds");
  box.replaceChildren();
  for (const k of a.kinds) {
    const lab = document.createElement("label");
    lab.className = "check-row";
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = k.enabled;
    cb.dataset.kind = k.id;
    lab.append(cb, document.createTextNode(" " + k.label));
    box.append(lab);
  }
  $("al-quiet").value = a.quiet_min;
  $("al-cpu").value = a.cpu_pct;
  $("al-mem").value = a.mem_pct;
  $("al-disk").value = a.disk_pct;
  $("al-cert").value = a.cert_days;
  $("al-label").value = a.device_label || "";

  const kv = $("al-state");
  kv.replaceChildren();
  const st = a.state || {};
  const rows = [["Javna IP adresa", a.public_ip || "još nije očitana"]];
  for (const [k, v] of Object.entries(st)) {
    if (k.startsWith("wan:")) rows.push(["Veza " + k.slice(4), v === "up" ? "radi" : "PALA"]);
    else if (k.startsWith("svc:")) rows.push(["Servis " + k.slice(4), v === "up" ? "radi" : "NE RADI"]);
    else if (k === "cgnat") rows.push(["Iza tuđeg NAT-a", v === "da" ? "DA — objave izvana neće raditi" : "ne"]);
  }
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
}

$("al-save").addEventListener("click", async () => {
  const kinds = {};
  for (const cb of $("al-kinds").querySelectorAll("input[type=checkbox]")) {
    kinds[cb.dataset.kind] = cb.checked;
  }
  $("al-result").textContent = "Spremam…";
  try {
    await api("/alerts", "POST", {
      kinds,
      quiet_min: parseInt($("al-quiet").value, 10) || 30,
      cpu_pct: parseInt($("al-cpu").value, 10) || 90,
      mem_pct: parseInt($("al-mem").value, 10) || 90,
      disk_pct: parseInt($("al-disk").value, 10) || 90,
      cert_days: parseInt($("al-cert").value, 10) || 30,
      device_label: $("al-label").value.trim(),
    });
    $("al-result").textContent = "Spremljeno.";
  } catch (e) {
    $("al-result").textContent = "Greška: " + (e.message || e);
  }
});

$("al-run").addEventListener("click", async () => {
  $("al-result").textContent = "Provjeravam…";
  try {
    const r = await api("/alerts/run", "POST", {});
    $("al-result").textContent = "Provjereno. Javna adresa: " +
      (r.public_ip || "nije očitana");
    await loadAlerts();
    await loadMonitorx();
  } catch (e) {
    $("al-result").textContent = "Greška: " + (e.message || e);
  }
});

/* ---------- trajni log (Postavke) ---------- */

async function loadLogStore(sys) {
  const ls = (sys && sys.logstore) || {};
  $("ls-enabled").checked = !!ls.enabled;
  $("ls-buffer").value = ls.buffer_kb || 1024;
  $("ls-keep").value = ls.keep_days || 30;

  const kv = $("ls-kv");
  kv.replaceChildren();
  for (const [k, v] of [
    ["Spremljeno datoteka", String(ls.files || 0)],
    ["Zauzeto na disku", fmtBytes(ls.size_bytes || 0)],
  ]) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }

  const tb = $("ls-rows");
  tb.replaceChildren();
  if (!ls.enabled) return;
  const f = await api("/system/logfiles").catch(() => ({ files: [] }));
  for (const file of f.files) {
    const tr = document.createElement("tr");
    for (const v of [file.name, fmtBytes(file.size), file.mtime]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    const td = document.createElement("td");
    td.append(btnSm("Preuzmi", false, () => downloadLog(file.name)));
    tr.append(td);
    tb.append(tr);
  }
}

async function downloadLog(name) {
  const blob = await apiBlob("/system/logfiles/" + encodeURIComponent(name));
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

$("ls-save").addEventListener("click", async () => {
  $("ls-result").textContent = "Spremam…";
  try {
    await api("/system/logstore", "POST", {
      enabled: $("ls-enabled").checked,
      buffer_kb: parseInt($("ls-buffer").value, 10) || 1024,
      keep_days: parseInt($("ls-keep").value, 10) || 30,
    });
    $("ls-result").textContent = "Spremljeno.";
    await loadSettings();
  } catch (e) {
    $("ls-result").textContent = "Greška: " + (e.message || e);
  }
});

/* ---------- backup izvan uređaja ---------- */

async function loadOffsite() {
  const o = await api("/backup/offsite");
  $("os-enabled").checked = !!o.enabled;
  $("os-host").value = o.host || "";
  $("os-port").value = o.port || 22;
  $("os-user").value = o.user || "";
  $("os-path").value = o.path || "";
  $("os-encrypt").checked = o.encrypt !== false;
  $("os-pubkey").textContent = o.public_key ||
    "— (ključ se napravi kad prvi put spremiš uključeno slanje)";
  $("os-pass").placeholder = o.has_pass
    ? "prazno = zadrži postojeću lozinku" : "obavezno pri prvom spremanju";

  const kv = $("os-kv");
  kv.replaceChildren();
  for (const [k, v] of [
    ["Zadnje uspješno slanje", o.last_ok || "još nijedno"],
    ["Zadnja greška", o.last_error || "nema"],
  ]) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
}

$("os-save").addEventListener("click", async () => {
  $("os-result").textContent = "Spremam…";
  try {
    await api("/backup/offsite", "POST", {
      enabled: $("os-enabled").checked,
      host: $("os-host").value.trim(),
      port: parseInt($("os-port").value, 10) || 22,
      user: $("os-user").value.trim(),
      path: $("os-path").value.trim(),
      encrypt: $("os-encrypt").checked,
      passphrase: $("os-pass").value,
    });
    $("os-pass").value = "";
    $("os-result").textContent = "Spremljeno. Ne zaboravi dodati javni ključ na poslužitelj.";
    await loadOffsite();
  } catch (e) {
    $("os-result").textContent = "Greška: " + (e.message || e);
  }
});

$("os-test").addEventListener("click", async () => {
  $("os-result").textContent = "Šaljem…";
  try {
    const r = await api("/backup/offsite/test", "POST", {});
    $("os-result").textContent = "Poslano: " + r.sent;
    await loadOffsite();
  } catch (e) {
    $("os-result").textContent = "Greška: " + (e.message || e);
  }
});

/* ---------- hardening: dodatne mjere zaštite ---------- */

async function loadHardening() {
  const h = await api("/hardening");
  const box = $("hd-items");
  box.replaceChildren();
  const on = h.items.filter((i) => i.enabled).length;
  setNote("hd-note", `uključeno ${on} od ${h.items.length} mjera`);
  for (const it of h.items) {
    const wrap = document.createElement("div");
    wrap.className = "hd-item";

    const lab = document.createElement("label");
    lab.className = "check-row";
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = it.enabled;
    cb.dataset.id = it.id;
    cb.dataset.was = it.enabled ? "1" : "0";
    lab.append(cb, document.createTextNode(" " + it.label));

    const det = document.createElement("p");
    det.className = "hint";
    det.textContent = it.detail + (it.note ? " (" + it.note + ")" : "");

    wrap.append(lab, det);
    box.append(wrap);
  }
}

$("hd-save").addEventListener("click", async () => {
  const items = {};
  for (const cb of $("hd-items").querySelectorAll("input[type=checkbox]")) {
    // šalju se samo promijenjene stavke — da se ne dira ono što je već dobro
    if ((cb.dataset.was === "1") !== cb.checked) items[cb.dataset.id] = cb.checked;
  }
  if (!Object.keys(items).length) {
    $("hd-result").textContent = "Nema promjena.";
    return;
  }
  $("hd-result").textContent = "Primjenjujem…";
  try {
    const r = await api("/hardening", "POST", { items });
    $("hd-result").textContent = "Primijenjeno: " + r.applied.join(", ");
    await loadHardening();
  } catch (e) {
    $("hd-result").textContent = "Greška: " + (e.message || e);
    await loadHardening();
  }
});

/* ---------- detekcija skeniranja portova (Blokade) ---------- */

function fillScan(sc) {
  if (!sc) return;
  $("sc-enabled").checked = !!sc.enabled;
  $("sc-rate").value = sc.rate;
  $("sc-burst").value = sc.burst;
  $("sc-ban").value = sc.ban_minutes;
  $("sc-allow").value = sc.allow_ips || "";

  const kv = $("sc-kv");
  kv.replaceChildren();
  const blocked = sc.blocked || [];
  for (const [k, v] of [
    ["Nadzirana sučelja", (sc.devices || []).join(", ") || "—"],
    ["Trenutno blokirano izvora", String(blocked.length)],
    ["Blokirani", blocked.length ? blocked.join(", ") : "nijedan"],
  ]) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    kv.append(dt, dd);
  }
}

$("sc-save").addEventListener("click", async () => {
  $("sc-result").textContent = "Spremam…";
  try {
    const sc = await api("/protection/scan", "POST", {
      enabled: $("sc-enabled").checked,
      rate: parseInt($("sc-rate").value, 10) || 25,
      burst: parseInt($("sc-burst").value, 10) || 50,
      ban_minutes: parseInt($("sc-ban").value, 10) || 60,
      allow_ips: $("sc-allow").value.trim(),
    });
    fillScan(sc);
    $("sc-result").textContent = "Spremljeno i primijenjeno.";
  } catch (e) {
    $("sc-result").textContent = "Greška: " + (e.message || e);
  }
});

$("sc-clear").addEventListener("click", async () => {
  try {
    await api("/protection/scan/clear", "POST", {});
    $("sc-result").textContent = "Popis blokiranih je ispražnjen.";
    loadProtection().catch(() => {});
  } catch (e) {
    $("sc-result").textContent = "Greška: " + (e.message || e);
  }
});

/* ---------- promjene konfiguracije (Nadzor) ---------- */

async function loadAudit(data) {
  const a = data || await api("/audit");
  const tb = $("au-rows");
  tb.replaceChildren();
  for (const c of a.changes) {
    const tr = document.createElement("tr");
    tr.style.cursor = "pointer";
    const who = c.source ? c.source : "izvan Saguara (LuCI/SSH)";
    for (const v of [c.ts, c.name, who, `+${c.added} / −${c.removed}`]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    tr.addEventListener("click", () => showDiff(c.id));
    tb.append(tr);
  }
  if (!a.changes.length) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 4;
    td.className = "hint";
    td.textContent = "Još nije zabilježena nijedna promjena.";
    tr.append(td);
    tb.append(tr);
  }
}

async function showDiff(id) {
  const pre = $("au-diff");
  pre.classList.remove("hidden");
  pre.textContent = "Učitavam…";
  try {
    const d = await api("/audit/" + id);
    const who = d.source ? "korisnik " + d.source : "izvan Saguara (LuCI ili SSH)";
    pre.textContent = `${d.name} — ${d.ts} — ${who}\n\n${d.diff}`;
  } catch (e) {
    pre.textContent = "Greška: " + (e.message || e);
  }
}

$("au-run").addEventListener("click", async () => {
  try {
    await loadAudit(await api("/audit/run", "POST", {}));
  } catch (e) {
    alertErr(e);
  }
});

/* ---------- učitavači novih modula (preraspodjela sučelja) ---------- */

// Upozorenja: vrste i pragovi + SMTP postavke na jednom mjestu.
async function loadAlertsView() {
  const [, mon] = await Promise.all([loadAlerts(), api("/monitor")]);
  const em = mon.email || {};
  $("sm-enabled").checked = !!em.enabled;
  $("sm-host").value = em.host || "";
  $("sm-port").value = em.port || "587";
  $("sm-user").value = em.user || "";
  $("sm-from").value = em.from || "";
  $("sm-to").value = em.to || "";
}

// System access: mjere zaštite (hardening) + tko smije do upravljanja (ACL).
async function loadHardeningView() {
  const [, sys] = await Promise.all([loadHardening(), api("/settings/system")]);
  const acl = sys.mgmt_acl || {};
  $("acl-enabled").checked = !!acl.enabled;
  $("acl-allow").value = acl.allow || "";
}

// Logovi: živi sustavski log, trajno spremanje i slanje na vanjski poslužitelj.
async function loadLogsView() {
  const sys = await api("/settings/system");
  const sl = sys.syslog || {};
  $("sl-enabled").checked = !!sl.enabled;
  $("sl-host").value = sl.host || "";
  $("sl-port").value = sl.port || "514";
  $("sl-proto").value = sl.proto || "udp";
  await loadLogStore(sys);
  refreshSyslog();
}

function refreshSyslog() {
  api("/syslog").then((sl) => {
    const el = $("sy-log");
    el.textContent = sl.log || "—";
    el.scrollTop = el.scrollHeight;
  }).catch(() => { $("sy-log").textContent = "Log nedostupan."; });
}

/* ---------- tražilica modula ---------- */

// Traži se i po nazivu i po opisu modula, pa "skenir" nađe Blokade iako se
// modul tako ne zove. Bez tražilice se na 22 modula gubi vrijeme na
// prisjećanje u kojoj je skupini nešto.
function searchModules(q) {
  q = q.trim().toLowerCase();
  if (!q) return [];
  const hits = [];
  for (const [id, m] of Object.entries(MODULES)) {
    const name = m[0].toLowerCase();
    const desc = m[1].toLowerCase();
    const keys = (MODULE_KEYS[id] || "").toLowerCase();
    let score = -1;
    if (name.startsWith(q)) score = 0;
    else if (name.includes(q)) score = 1;
    else if (keys.includes(q)) score = 2;
    else if (desc.includes(q)) score = 3;
    if (score >= 0) hits.push({ id, name: m[0], desc: m[1], score });
  }
  hits.sort((a, b) => a.score - b.score || a.name.localeCompare(b.name, "hr"));
  return hits.slice(0, 8);
}

function renderSearch(hits, sel) {
  const box = $("nav-results");
  box.replaceChildren();
  if (!hits.length) {
    box.classList.add("hidden");
    return;
  }
  hits.forEach((h, i) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "navresult" + (i === sel ? " sel" : "");
    const grp = NAV_GROUPS[groupOf(h.id)];
    const nm = document.createElement("span");
    nm.className = "nr-name";
    nm.textContent = h.name;
    const gr = document.createElement("span");
    gr.className = "nr-group";
    gr.textContent = grp ? grp[0] : "";
    b.append(nm, gr);
    b.addEventListener("mousedown", (ev) => {
      ev.preventDefault();
      gotoModule(h.id);
    });
    box.append(b);
  });
  box.classList.remove("hidden");
}

function gotoModule(id) {
  $("nav-search").value = "";
  $("nav-results").classList.add("hidden");
  location.hash = "#" + id;
}

let searchSel = 0;
$("nav-search").addEventListener("input", () => {
  searchSel = 0;
  renderSearch(searchModules($("nav-search").value), searchSel);
});
$("nav-search").addEventListener("keydown", (ev) => {
  const hits = searchModules($("nav-search").value);
  if (!hits.length) return;
  if (ev.key === "ArrowDown") {
    ev.preventDefault();
    searchSel = (searchSel + 1) % hits.length;
    renderSearch(hits, searchSel);
  } else if (ev.key === "ArrowUp") {
    ev.preventDefault();
    searchSel = (searchSel - 1 + hits.length) % hits.length;
    renderSearch(hits, searchSel);
  } else if (ev.key === "Enter") {
    ev.preventDefault();
    gotoModule(hits[searchSel].id);
  } else if (ev.key === "Escape") {
    $("nav-search").value = "";
    $("nav-results").classList.add("hidden");
  }
});
$("nav-search").addEventListener("blur", () => {
  setTimeout(() => $("nav-results").classList.add("hidden"), 120);
});
