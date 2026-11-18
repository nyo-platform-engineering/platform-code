"use strict";
const $ = (id) => document.getElementById(id);
const kinds = [
  ["SEISMIC", "Gempa", "M2 12h4l2-6 4 13 3-9 2 2h5"],
  [
    "TSUNAMI",
    "Tsunami",
    "M2 16c4-8 6 2 11-3s7-7 9-3M2 20c4-4 6 3 11 0s7-4 9-1M8 9c2-7 11-6 10 0-2-2-4-1-4 1",
  ],
  [
    "VOLCANIC",
    "Gunung berapi",
    "M3 20 9 9h6l3 11ZM9 9l3 4 3-4M11 5c-3-3 3-3 0-5M16 5c3-2-1-3 1-4",
  ],
  [
    "WILDFIRE",
    "Kebakaran hutan",
    "M13 2c2 5-1 5 3 8 2-1 2-3 2-3 6 8 2 14-6 14-7 0-10-7-5-12 0 3 2 4 3 3 2-2 0-5 3-10Z",
  ],
  [
    "COASTAL",
    "Pesisir",
    "M3 15c3-3 5 3 8 0s5 3 10 0M3 20c3-3 5 3 8 0s5 3 10 0M6 10V3h12v7M9 6h6",
  ],
];
const number = new Intl.NumberFormat("id-ID");
const percent = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });
const dateLabel = new Intl.DateTimeFormat("id-ID", {
  day: "numeric",
  month: "short",
});
const timeLabel = new Intl.DateTimeFormat("id-ID", {
  hour: "2-digit",
  minute: "2-digit",
});
const intervalNames = {
  hour: "jam",
  day: "hari",
  week: "minggu",
  month: "bulan",
  year: "tahun",
};
const bucketDate = new Intl.DateTimeFormat("id-ID", {
  day: "numeric",
  month: "short",
  timeZone: "UTC",
});
const bucketTime = new Intl.DateTimeFormat("id-ID", {
  hour: "2-digit",
  minute: "2-digit",
  timeZone: "UTC",
});
const bucketMonth = new Intl.DateTimeFormat("id-ID", {
  month: "short",
  year: "2-digit",
  timeZone: "UTC",
});
const longDate = new Intl.DateTimeFormat("id-ID", {
  day: "numeric",
  month: "short",
  year: "numeric",
});
function axisLabel(at, interval) {
  if (interval === "year") return String(at.getUTCFullYear());
  if (interval === "month") return bucketMonth.format(at);
  return interval === "hour" ? bucketTime.format(at) : bucketDate.format(at);
}
const state = {
  pair: null,
  expiry: 0,
  lastGood: null,
  active: false,
  request: null,
  sequence: 0,
  authVersion: 0,
  refreshing: null,
};
const svgNS = "http://www.w3.org/2000/svg";
function node(tag, cls, text) {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}
function svg(tag, attrs = {}, text) {
  const el = document.createElementNS(svgNS, tag);
  Object.entries(attrs).forEach(([k, v]) => el.setAttribute(k, v));
  if (text != null) el.textContent = text;
  return el;
}
function typeName(key) {
  return kinds.find((x) => x[0] === key)?.[1] || key;
}
function countMap(items) {
  return new Map((items || []).map((x) => [x.key, x.count]));
}

function renderCards(data) {
  const counts = countMap(data?.by_type);
  $("type-cards").replaceChildren(
    ...kinds.map(([key, name, path]) => {
      const card = node("article", `type-card tone-${key}`);
      const head = node("div", "type-card-head");
      head.append(node("span", "type-name", name));
      const icon = node("span", "type-icon");
      icon.setAttribute("aria-hidden", "true");
      const image = svg("svg", { viewBox: "0 0 24 24" });
      image.append(svg("path", { d: path }));
      icon.append(image);
      head.append(icon);
      card.append(
        head,
        node(
          "div",
          "type-number",
          data ? number.format(counts.get(key) || 0) : "—",
        ),
        node(
          "div",
          "type-caption",
          key === "TSUNAMI"
            ? "Subset gempa dengan warning"
            : "kejadian tercatat",
        ),
      );
      return card;
    }),
  );
  $("total").textContent = data ? `${number.format(data.total)} total` : "—";
}
function renderTrend(data) {
  const host = $("trend");
  host.replaceChildren();
  if (!data || data.total === 0) {
    const empty = node("div", "chart-empty");
    empty.append(
      node(
        "strong",
        "",
        data ? "Belum ada kejadian" : "Ringkasan belum tersedia",
      ),
      node(
        "span",
        "",
        data
          ? "Coba rentang waktu atau sumber yang berbeda."
          : "Data akan tampil setelah berhasil dimuat.",
      ),
    );
    host.append(empty);
    return;
  }
  const buckets = data.timeline;
  const style = getComputedStyle(host);
  const width = Math.max(
      280,
      host.clientWidth -
        parseFloat(style.paddingLeft) -
        parseFloat(style.paddingRight),
    ),
    height = Math.max(
      180,
      host.clientHeight -
        parseFloat(style.paddingTop) -
        parseFloat(style.paddingBottom),
    ),
    left = 45,
    right = 14,
    top = 15,
    bottom = 34;
  const plotW = width - left - right,
    plotH = height - top - bottom;
  const highest = Math.max(
    1,
    ...buckets.flatMap((b) => Object.values(b.counts)),
  );
  const scale = 10 ** Math.floor(Math.log10(highest));
  const max = Math.ceil(highest / scale / 2) * scale * 2;
  const image = svg("svg", {
    viewBox: `0 0 ${width} ${height}`,
    role: "img",
    "aria-label": `Tren kejadian per ${intervalNames[data.interval]}, ${dateLabel.format(new Date(data.from))} sampai ${dateLabel.format(new Date(data.to))}. Rincian tersedia melalui Unduh CSV.`,
  });
  const x = (i) =>
    left +
    (buckets.length === 1 ? plotW / 2 : (i / (buckets.length - 1)) * plotW);
  const y = (n) => top + plotH - (n / max) * plotH;
  for (let i = 0; i <= 4; i++) {
    const value = (max * i) / 4;
    const yy = y(value);
    image.append(
      svg("line", {
        x1: left,
        y1: yy,
        x2: width - right,
        y2: yy,
        class: "chart-gridline",
      }),
      svg(
        "text",
        { x: left - 12, y: yy + 4, "text-anchor": "end", class: "chart-tick" },
        number.format(Math.round(value)),
      ),
    );
  }
  const labels = Math.min(buckets.length, width < 500 ? 4 : 6);
  for (let n = 0; n < labels; n++) {
    const i =
      labels === 1 ? 0 : Math.round((n * (buckets.length - 1)) / (labels - 1));
    const at = new Date(buckets[i].at);
    image.append(
      svg(
        "text",
        {
          x: x(i),
          y: height - 7,
          "text-anchor":
            n === 0 ? "start" : n === labels - 1 ? "end" : "middle",
          class: "chart-tick",
        },
        axisLabel(at, data.interval),
      ),
    );
  }
  for (const [key, name] of kinds) {
    if (!buckets.some((b) => b.counts[key] > 0)) continue;
    const points = buckets
      .map((b, i) => `${x(i)},${y(b.counts[key])}`)
      .join(" ");
    const group = svg("g", { class: `tone-${key}` });
    group.append(
      svg("polygon", {
        points: `${x(0)},${y(0)} ${points} ${x(buckets.length - 1)},${y(0)}`,
        class: "chart-area",
      }),
      svg("polyline", { points, class: "chart-line" }),
    );
    buckets.forEach((b, i) => {
      const dot = svg("circle", {
        cx: x(i),
        cy: y(b.counts[key]),
        r: 3,
        class: "chart-point",
      });
      dot.append(
        svg(
          "title",
          {},
          `${name} · ${new Date(b.at).toLocaleString("id-ID", { timeZone: "UTC" }) + " UTC"} · ${number.format(b.counts[key])} kejadian`,
        ),
      );
      group.append(dot);
    });
    image.append(group);
  }
  host.append(image);
}
function renderSeverity(data) {
  const counts = countMap(data?.by_severity);
  $("severity").replaceChildren(
    ...["NORMAL", "WASPADA", "SIAGA", "AWAS"].map((key) => {
      const row = node("div", `severity-row severity-${key}`),
        head = node("div", "severity-head");
      head.append(
        node("span", "", key.charAt(0) + key.slice(1).toLowerCase()),
        node("strong", "", data ? number.format(counts.get(key) || 0) : "—"),
      );
      const track = node("div", "severity-track"),
        bar = node("div", "severity-bar");
      bar.style.width = `${data?.total ? ((counts.get(key) || 0) / data.total) * 100 : 0}%`;
      track.append(bar);
      row.append(head, track);
      return row;
    }),
  );
}
function renderAreas(data) {
  const query = $("area-search").value.trim().toLocaleLowerCase("id-ID");
  const areas = (data?.by_area || []).filter((a) =>
    a.key.toLocaleLowerCase("id-ID").includes(query),
  );
  $("areas").replaceChildren(
    ...areas.map((a) => {
      const row = node("tr"),
        name = node("td", "", a.key || "Tidak diketahui"),
        count = node("td", "count-column", number.format(a.count)),
        prop = node("td", "share-column");
      const ratio = data.total ? (a.count / data.total) * 100 : 0,
        share = node("div", "share"),
        track = node("div", "share-track"),
        fill = node("div", "share-fill");
      fill.style.width = `${ratio}%`;
      track.append(fill);
      share.append(
        track,
        node("span", "share-value", `${percent.format(ratio)}%`),
      );
      prop.append(share);
      row.append(name, count, prop);
      return row;
    }),
  );
  if (!areas.length) {
    const row = node("tr"),
      cell = node(
        "td",
        "empty-row",
        !data
          ? "Data belum tersedia."
          : query
            ? "Wilayah tidak ditemukan."
            : "Belum ada kejadian pada periode ini.",
      );
    cell.colSpan = 3;
    row.append(cell);
    $("areas").append(row);
  }
  $("area-count").textContent = data
    ? `${number.format(areas.length)} dari ${number.format(data.by_area.length)} wilayah`
    : "Wilayah akan tampil setelah data tersedia.";
}
function renderSources(sources = []) {
  $("sources").replaceChildren(
    ...[
      ["BMKG", "BMKG"],
      ["PVMBG", "PVMBG"],
      ["SATELIT", "Satelit"],
      ["SENSOR_PANTAI", "Sensor Pantai"],
    ].map(([key, label]) => {
      const source = sources.find((s) => s.source === key),
        row = node("div", "source-row"),
        text = node("div");
      const healthy = source?.status === "fresh" && !source.stale,
        unknown = !source || source.status === "unknown";
      text.append(
        node("div", "source-name", label),
        node(
          "div",
          "source-sub",
          source?.last_success_at
            ? `Sukses ${timeLabel.format(new Date(source.last_success_at))}`
            : "Belum ada pembaruan",
        ),
      );
      row.append(
        text,
        node(
          "span",
          `source-state ${unknown ? "unknown" : healthy ? "" : "bad"}`,
          unknown
            ? "Belum ada status"
            : healthy
              ? "Tersedia"
              : source.stale
                ? "Terlambat"
                : "Bermasalah",
        ),
      );
      return row;
    }),
  );
}
function paint(data) {
  renderCards(data);
  renderTrend(data);
  renderSeverity(data);
  renderAreas(data);
  renderSources(data?.sources);
  const rangeFormat =
    data && new Date(data.to) - new Date(data.from) > 366 * 86400000
      ? longDate
      : dateLabel;
  $("range-label").textContent = data
    ? `${rangeFormat.format(new Date(data.from))} – ${rangeFormat.format(new Date(data.to))}`
    : "—";
  $("interval-label").textContent = data
    ? `Jumlah kejadian per ${intervalNames[data.interval]} · UTC`
    : "Jumlah kejadian menurut waktu";
}
function notice(message, error = false) {
  $("notice").textContent = message;
  $("notice").className = `notice${error ? " error" : ""}`;
  $("notice").hidden = !message;
}
function status(text, cls = "") {
  $("data-status").textContent = text;
  $("data-status").className = `status-pill ${cls}`;
}
function showLogin(message = "") {
  state.authVersion++;
  state.sequence++;
  state.refreshing = null;
  state.active = false;
  state.pair = null;
  state.lastGood = null;
  state.request?.abort();
  $("dashboard").hidden = true;
  $("login").hidden = false;
  $("logout").hidden = true;
  $("login-error").textContent = message;
  $("secret").focus();
}
async function session(path, body) {
  const version = state.authVersion;
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    cache: "no-store",
  });
  if (!response.ok) {
    const error = new Error(
      response.status === 401
        ? "Kunci akses tidak sesuai atau sesi telah berakhir."
        : "Layanan masuk belum tersedia. Coba kembali sebentar lagi.",
    );
    error.status = response.status;
    throw error;
  }
  const pair = await response.json();
  if (version !== state.authVersion)
    throw new DOMException("Sesi dibatalkan", "AbortError");
  state.pair = pair;
  state.expiry = Date.now() + pair.expires_in * 1000;
  return pair;
}
async function access(force = false) {
  if (!state.pair) throw new Error("Sesi telah berakhir.");
  if (force || Date.now() > state.expiry - 5000) {
    if (!state.refreshing) {
      const pending = session("/session/refresh", {
        refresh_token: state.pair.refresh_token,
      });
      state.refreshing = pending;
      pending
        .finally(() => {
          if (state.refreshing === pending) state.refreshing = null;
        })
        .catch(() => {});
    }
    await state.refreshing;
  }
  if (!state.pair) throw new DOMException("Sesi dibatalkan", "AbortError");
  return state.pair.access_token;
}

function query() {
  let to = new Date(),
    from;
  if ($("range").value === "custom") {
    from = new Date($("from").value);
    to = new Date($("to").value);
  } else from = new Date(to.getTime() - Number($("range").value) * 3600000);
  if (
    !Number.isFinite(from.getTime()) ||
    !Number.isFinite(to.getTime()) ||
    to <= from
  )
    throw new Error("Pilih waktu mulai sebelum waktu akhir.");
  return new URLSearchParams({
    from: from.toISOString(),
    to: to.toISOString(),
    source: $("source").value,
    interval:
      to - from <= 72 * 3600000
        ? "hour"
        : to - from <= 366 * 86400000
          ? "day"
          : to - from <= 20 * 365 * 86400000
            ? "month"
            : "year",
  });
}
async function load(clear = false) {
  if (!state.active) return;
  state.request?.abort();
  state.request = new AbortController();
  const signal = state.request.signal,
    sequence = ++state.sequence;
  if (clear) {
    state.lastGood = null;
    paint(null);
    $("updated").textContent = "—";
  }
  $("refresh").disabled = true;
  $("charts").setAttribute("aria-busy", "true");
  $("export").disabled = true;
  status("Memuat data", "neutral");
  notice("");
  try {
    const params = query();
    let token = await access();
    let response = await fetch(`/analytics?${params}`, {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
      signal,
    });
    if (response.status === 401) {
      token = await access(true);
      response = await fetch(`/analytics?${params}`, {
        headers: { Authorization: `Bearer ${token}` },
        cache: "no-store",
        signal,
      });
    }
    if (response.status === 401) {
      showLogin("Sesi telah berakhir. Silakan masuk kembali.");
      return;
    }
    if (!response.ok)
      throw new Error(
        response.status === 400
          ? "Rentang waktu tidak valid atau melebihi 1.000 bucket grafik."
          : "Analitik belum dapat dimuat. Coba perbarui beberapa saat lagi.",
      );
    const data = await response.json();
    if (sequence !== state.sequence) return;
    if (data.partial) {
      $("charts").classList.add("stale");
      status("Data parsial", "warn");
      notice(
        state.lastGood
          ? "Sebagian penyimpanan belum tersedia. Grafik menampilkan snapshot terakhir dan belum mencakup pembaruan terbaru."
          : "Sebagian penyimpanan belum tersedia. Ringkasan belum dapat dihitung dengan lengkap; perbarui setelah koneksi pulih.",
      );
      renderSources(data.sources);
    } else {
      state.lastGood = data;
      paint(data);
      $("charts").classList.remove("stale");
      status("Penyimpanan tersedia");
      $("updated").textContent =
        `Diperbarui ${timeLabel.format(new Date(data.as_of))}`;
      $("export").disabled = false;
    }
  } catch (error) {
    if (error.name === "AbortError" || sequence !== state.sequence) return;
    if (error.status === 401) {
      showLogin("Sesi telah berakhir. Silakan masuk kembali.");
      return;
    }
    status("Belum tersedia", "error");
    $("charts").classList.add("stale");
    notice(
      error.message +
        (state.lastGood ? " Grafik masih menampilkan snapshot terakhir." : ""),
      true,
    );
  } finally {
    if (sequence === state.sequence) {
      $("refresh").disabled = false;
      $("charts").setAttribute("aria-busy", "false");
    }
  }
}
$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  $("login-button").disabled = true;
  $("login-error").textContent = "";
  try {
    await session("/session", { client_secret: $("secret").value });
    $("secret").value = "";
    state.active = true;
    $("login").hidden = true;
    $("dashboard").hidden = false;
    $("logout").hidden = false;
    paint(null);
    await load(true);
  } catch (error) {
    $("login-error").textContent = error.message;
  } finally {
    $("login-button").disabled = false;
  }
});
$("logout").addEventListener("click", () => showLogin());
$("refresh").addEventListener("click", () => load());
$("source").addEventListener("change", () => load(true));
$("range").addEventListener("change", () => {
  const custom = $("range").value === "custom";
  $("custom-dates").hidden = !custom;
  for (const id of ["from", "to"]) $(id).required = custom;
  if (!custom) load(true);
});
$("filters").addEventListener("submit", (event) => {
  event.preventDefault();
  load(true);
});
$("area-search").addEventListener("input", () => renderAreas(state.lastGood));
$("export").addEventListener("click", () => {
  const d = state.lastGood;
  if (!d || $("export").disabled) return;
  const rows = [["dimensi", "kategori", "jumlah", "dari_utc", "sampai_utc"]];
  for (const [dimension, entries] of [
    ["jenis", d.by_type],
    ["severity", d.by_severity],
    ["wilayah", d.by_area],
  ])
    for (const item of entries)
      rows.push([dimension, item.key, item.count, d.from, d.to]);
  for (const bucket of d.timeline)
    for (const [kind, count] of Object.entries(bucket.counts))
      rows.push(["waktu", `${bucket.at} ${kind}`, count, d.from, d.to]);
  const csv = rows
    .map((row) =>
      row
        .map((v) => {
          const text = String(v);
          return `"${(/^\s*[=+@-]|^[\t\r]/.test(text) ? "'" + text : text).replaceAll('"', '""')}"`;
        })
        .join(","),
    )
    .join("\r\n");
  const url = URL.createObjectURL(
      new Blob(["\ufeff", csv], { type: "text/csv;charset=utf-8" }),
    ),
    link = node("a");
  link.href = url;
  link.download = "bnpb-analitik.csv";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});
const legend = $("legend");
for (const [key, name] of kinds) {
  const item = node("span", `legend-item tone-${key}`);
  item.append(node("span", "legend-dot"), node("span", "", name));
  legend.append(item);
}
const now = new Date();
function localInput(d) {
  const adjusted = new Date(d.getTime() - d.getTimezoneOffset() * 60000);
  return adjusted.toISOString().slice(0, 16);
}
$("to").value = localInput(now);
$("from").value = localInput(new Date(now.getTime() - 7 * 86400000));
setInterval(() => {
  if (state.active && !document.hidden) load();
}, 30000);

let resizeFrame;
window.addEventListener("resize", () => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(() => {
    if (state.active) renderTrend(state.lastGood);
  });
});
