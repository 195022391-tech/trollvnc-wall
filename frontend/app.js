import { createPlayer, destroyPlayer, isSupported, shouldRebuildOnParams } from "./player.js";

const API = "";
const grid = document.getElementById("grid");
const metricsEl = document.getElementById("metrics");
const dlg = document.getElementById("dlg");
const PAGE_SIZE = 32;

let sessionId = localStorage.getItem("wallSession") || "";
let pageVisible = document.visibilityState !== "hidden";
const cells = new Map(); // deviceId -> cell state

async function api(path, body) {
  const res = await fetch(API + path, {
    method: body ? "POST" : "GET",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

async function ensureSession() {
  if (sessionId) {
    try {
      await api("/api/session/subscribe", { sessionId });
      return;
    } catch (_) {}
  }
  const r = await api("/api/session/subscribe", {});
  sessionId = r.sessionId;
  localStorage.setItem("wallSession", sessionId);
}

function unsubscribe() {
  if (!sessionId) return;
  navigator.sendBeacon?.(
    API + "/api/session/unsubscribe",
    new Blob([JSON.stringify({ sessionId })], { type: "application/json" })
  );
  fetch(API + "/api/session/unsubscribe", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sessionId }),
    keepalive: true,
  }).catch(() => {});
}

function setVisibility(v) {
  pageVisible = v;
  if (sessionId) {
    api("/api/session/visibility", { sessionId, pageVisible: v }).catch(() => {});
  }
}

document.addEventListener("visibilitychange", () => {
  setVisibility(document.visibilityState !== "hidden");
});
window.addEventListener("pagehide", unsubscribe);
window.addEventListener("beforeunload", unsubscribe);

function ensureCell(snap) {
  let st = cells.get(snap.deviceId);
  if (st) return st;
  const el = document.createElement("div");
  el.className = "cell offline";
  el.dataset.id = snap.deviceId;
  el.innerHTML = `
    <video playsinline muted></video>
    <div class="tag"></div>
    <div class="mask">离线</div>
    <div class="hud">
      <button type="button" class="focus ghost">焦点</button>
      <button type="button" class="hd">高清</button>
      <button type="button" class="prev ghost">预览</button>
    </div>`;
  const video = el.querySelector("video");
  st = {
    el, video, snap, player: null, destroyed: true,
    switchSeq: 0, width: 0, height: 0, observer: null,
  };
  el.querySelector(".focus").onclick = () => api("/api/focus", { deviceId: snap.deviceId });
  el.querySelector(".hd").onclick = async () => {
    if (!pageVisible) return alert("页面不可见，禁止申请高清");
    try {
      const r = await api("/api/hd/acquire", {
        deviceId: snap.deviceId, sessionId, pageVisible,
      });
      if (!r.ok) {
        alert(r.error || "HD 失败");
        return;
      }
      st.switchSeq = r.switchSeq;
      // 禁止失败后自动重试（定稿）
    } catch (e) {
      alert(e.message);
    }
  };
  el.querySelector(".prev").onclick = () =>
    api("/api/hd/release", { deviceId: snap.deviceId });
  grid.appendChild(el);
  cells.set(snap.deviceId, st);

  st.observer = new IntersectionObserver(async (entries) => {
    for (const en of entries) {
      if (en.isIntersecting) await startPlay(st);
      else await stopPlay(st);
    }
  }, { threshold: 0.15 });
  st.observer.observe(el);
  return st;
}

async function stopPlay(st) {
  if (st.destroyed && !st.player) return;
  st.destroyed = true;
  const seq = st.switchSeq;
  st.player = await destroyPlayer({
    player: st.player,
    videoEl: st.video,
    cancelSwitch: async () => {
      if (seq) {
        await api("/api/switch/cancel", { deviceId: st.snap.deviceId, switchSeq: seq }).catch(() => {});
      }
    },
    releaseTS: async () => {},
  });
}

async function startPlay(st) {
  if (!pageVisible) return;
  const snap = st.snap;
  if (snap.uiState === "offline" || !snap.streamOnline) {
    await stopPlay(st);
    return;
  }
  if (!isSupported()) {
    st.el.querySelector(".mask").textContent = "浏览器不支持 mpegts";
    return;
  }
  const needRebuild =
    !st.player ||
    st.destroyed ||
    shouldRebuildOnParams(
      { width: st.width, height: st.height },
      { width: snap.width || st.width, height: snap.height || st.height }
    );
  if (!needRebuild && st.player) return;

  await stopPlay(st);
  st.destroyed = false;
  st.width = snap.width || 1280;
  st.height = snap.height || 720;
  try {
    st.player = createPlayer(st.video, snap.playUrl);
    st.player.on(mpegts.Events.ERROR, async () => {
      if (st.destroyed) return;
      await stopPlay(st);
      if (!st.destroyed) await startPlay(st);
    });
    // 切换中：收到可播后上报 idr_seen
    st.player.on(mpegts.Events.MEDIA_INFO, async () => {
      if (st.destroyed) return;
      if (snap.uiState === "switching" && st.switchSeq) {
        await api("/api/switch/idr_seen", {
          deviceId: snap.deviceId,
          switchSeq: st.switchSeq,
          width: snap.width,
          height: snap.height,
          mode: "hd",
        }).catch(() => {});
      }
    });
  } catch (e) {
    st.el.querySelector(".mask").textContent = e.message;
    st.destroyed = true;
  }
}

function renderSnap(snap) {
  const st = ensureCell(snap);
  if (st.destroyed === false && st.player &&
      shouldRebuildOnParams(
        { width: st.width, height: st.height },
        { width: snap.width || st.width, height: snap.height || st.height }
      )) {
    // 分辨率变 → 重建
    startPlay(st);
  }
  st.snap = snap;
  if (snap.switchSeq) st.switchSeq = snap.switchSeq;
  st.el.className = "cell " + (snap.uiState || "offline");
  if (snap.isFocused) st.el.classList.add("focused");
  st.el.querySelector(".tag").textContent =
    `${snap.name || snap.deviceId} · ${snap.uiState}` + (snap.isFocused ? " · 焦点" : "");
  const mask = st.el.querySelector(".mask");
  if (snap.uiState === "offline") {
    mask.hidden = false;
    mask.textContent = "离线";
    stopPlay(st);
  } else if (snap.uiState === "switching") {
    mask.hidden = false;
    mask.textContent = "切换中…";
    startPlay(st);
  } else {
    mask.hidden = true;
    startPlay(st);
  }
}

async function refresh() {
  const data = await api("/api/devices");
  const list = (data.devices || []).slice(0, PAGE_SIZE);
  const seen = new Set();
  for (const d of list) {
    seen.add(d.deviceId);
    renderSnap(d);
  }
  for (const [id, st] of cells) {
    if (!seen.has(id)) {
      await stopPlay(st);
      st.observer?.disconnect();
      st.el.remove();
      cells.delete(id);
    }
  }
}

function bindSSE() {
  if (!sessionId) return;
  const es = new EventSource(API + "/api/events?sessionId=" + encodeURIComponent(sessionId));
  es.onmessage = (ev) => {
    try {
      const snap = JSON.parse(ev.data);
      const st = cells.get(snap.deviceId);
      if (st?.destroyed && snap.uiState !== "offline") {
        // 已销毁实例忽略方法调用；仅更新状态，由 observer 再拉流
      }
      renderSnap(snap);
    } catch (_) {}
  };
  es.onerror = () => {
    es.close();
    setTimeout(bindSSE, 2000);
  };
}

document.getElementById("btn-reg").onclick = () => dlg.showModal();
document.getElementById("reg-form").onsubmit = async (ev) => {
  if (ev.submitter?.value === "cancel") return;
  ev.preventDefault();
  const fd = new FormData(ev.target);
  await api("/api/devices/register", {
    deviceId: fd.get("deviceId"),
    token: fd.get("token"),
    name: fd.get("name"),
  });
  dlg.close();
  await refresh();
};

async function tickMetrics() {
  try {
    const m = await api("/api/metrics");
    metricsEl.textContent = `在线 ${m.online} · HD ${m.hd} · 切换 ${m.switching}`;
  } catch (_) {}
}

(async () => {
  if (!isSupported()) {
    metricsEl.textContent = "mpegts.js 不可用";
  }
  await ensureSession();
  setVisibility(pageVisible);
  await refresh();
  bindSSE();
  setInterval(tickMetrics, 2000);
  setInterval(refresh, 5000);
})();
