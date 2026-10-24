/** mpegts.js 封装：destroy 步步超时；仅宽高变 / 断流重建 */

const ABORT_MS = 800;
const REMOVE_MS = 800;

export function isSupported() {
  return typeof mpegts !== "undefined" && mpegts.isSupported && mpegts.isSupported();
}

export function createPlayer(videoEl, url) {
  if (!isSupported()) throw new Error("mpegts unsupported");
  const player = mpegts.createPlayer(
    { type: "mse", isLive: true, url },
    { enableStashBuffer: false, stashInitialSize: 128, liveBufferLatencyChasing: true }
  );
  player.attachMediaElement(videoEl);
  player.load();
  player.play().catch(() => {});
  return player;
}

function waitUpdateEnd(sb, ms) {
  return new Promise((resolve) => {
    if (!sb || !sb.updating) return resolve(true);
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      sb.removeEventListener("updateend", onEnd);
      resolve(ok);
    };
    const onEnd = () => finish(true);
    sb.addEventListener("updateend", onEnd);
    setTimeout(() => finish(false), ms);
  });
}

async function clearSourceBuffers(mediaSource, ms) {
  if (!mediaSource || !mediaSource.sourceBuffers) return;
  for (const sb of Array.from(mediaSource.sourceBuffers)) {
    try {
      if (sb.updating) {
        try {
          sb.abort();
        } catch (_) {}
        await waitUpdateEnd(sb, ms);
      }
      if (sb.buffered.length) {
        try {
          sb.remove(0, sb.buffered.end(sb.buffered.length - 1));
        } catch (_) {}
        await waitUpdateEnd(sb, ms);
      }
    } catch (_) {}
  }
}

/**
 * destroyPlayer 固定顺序（定稿）：
 * cancelSwitch → pause → unload → detach → abort/remove(超时) → destroy → 置空 → releaseTS
 */
export async function destroyPlayer(ctx) {
  const {
    player,
    videoEl,
    cancelSwitch,
    releaseTS,
  } = ctx;

  try {
    if (cancelSwitch) await cancelSwitch();
  } catch (_) {}

  try {
    if (player) {
      try { player.pause(); } catch (_) {}
      try { player.unload(); } catch (_) {}
      try { player.detachMediaElement(); } catch (_) {}
    }
  } catch (_) {}

  try {
    if (videoEl) {
      const mediaSource = videoEl._mediaSource || null;
      await clearSourceBuffers(mediaSource, REMOVE_MS);
    }
  } catch (_) {}

  // 超时兜底：直接 destroy
  await Promise.race([
    Promise.resolve().then(() => {
      try { if (player) player.destroy(); } catch (_) {}
    }),
    new Promise((r) => setTimeout(r, ABORT_MS + REMOVE_MS)),
  ]);

  if (videoEl) {
    try { videoEl.removeAttribute("src"); videoEl.load(); } catch (_) {}
  }

  try {
    if (releaseTS) await releaseTS();
  } catch (_) {}

  return null;
}

export function shouldRebuildOnParams(prev, next) {
  if (!prev) return true;
  return prev.width !== next.width || prev.height !== next.height;
}
