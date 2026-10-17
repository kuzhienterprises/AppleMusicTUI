// Spike 17: is anything audible at all from the hidden browser?
// Phase 1 plays a plain public MP3 (no Apple, no DRM).
// Phase 2 plays the DRM track through MusicKit.
// Also reports every mute/volume knob we can read.
import { spawn, spawnSync } from 'node:child_process';
import path from 'node:path';

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const PROFILE = path.join(process.env.APPDATA, 'applemusic-cli', 'browser-profile');
const PORT = 9570;
const MP3 = 'https://download.samplelib.com/mp3/sample-15s.mp3';
const SONG = '1097861836';

const chrome = spawn(CHROME, [
  `--user-data-dir=${PROFILE}`,
  `--remote-debugging-port=${PORT}`,
  '--remote-allow-origins=*',
  '--no-first-run', '--no-default-browser-check',
  '--autoplay-policy=no-user-gesture-required',
  '--disable-features=Translate,MediaRouter',
  '--window-size=400,300', '--window-position=-32000,-32000',
  'https://music.apple.com/in/browse',
], { stdio: ['ignore', 'ignore', 'pipe'] });
chrome.stderr.on('data', () => {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
setTimeout(() => { console.log('WATCHDOG'); process.exit(3); }, 240000).unref?.();

async function waitFor(fn, ms, label) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) { try { const v = await fn(); if (v) return v; } catch {} await sleep(500); }
  throw new Error('timed out waiting for ' + label);
}

try {
  await waitFor(async () => (await fetch(`http://127.0.0.1:${PORT}/json/version`)).ok, 60000, 'debugger');
  const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
  const page = list.find((t) => t.type === 'page' && /music\.apple\.com/.test(t.url)) || list.find((t) => t.type === 'page');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', () => rej(new Error('ws'))); });
  let id = 1; const pending = new Map();
  ws.addEventListener('message', (e) => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } });
  const call = (method, params = {}) => new Promise((res, rej) => {
    const myId = id++;
    const t = setTimeout(() => { pending.delete(myId); rej(new Error(method + ' timeout')); }, 20000);
    pending.set(myId, (m) => { clearTimeout(t); m.error ? rej(new Error(m.error.message)) : res(m.result); });
    ws.send(JSON.stringify({ id: myId, method, params }));
  });
  const ev = async (expr) => {
    const r = await call('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) return { __error: r.exceptionDetails.exception?.description || r.exceptionDetails.text };
    return r.result.value;
  };

  await call('Runtime.enable');
  await waitFor(async () => await ev(`typeof window.MusicKit !== 'undefined' && !!window.MusicKit.getInstance()`), 60000, 'MusicKit');
  await call('Page.bringToFront').catch(() => {});
  await call('Emulation.setFocusEmulationEnabled', { enabled: true }).catch(() => {});

  // Slow, obvious tone so "did you hear it" is unambiguous.
  console.log('\n=== PHASE 1: plain MP3 (not Apple, not DRM) — LISTEN NOW ===');
  await ev(`(() => {
    const a = document.createElement('audio');
    a.id = 'am-probe';
    a.src = ${JSON.stringify(MP3)};
    a.volume = 1;
    a.autoplay = true;
    document.body.appendChild(a);
    window.__probe = a;
    a.play().catch((e) => { window.__probeErr = String(e); });
    return true;
  })()`);
  for (let i = 0; i < 5; i++) {
    await sleep(2000);
    console.log('  ' + JSON.stringify(await ev(`(() => { const a = window.__probe; return { t: +(a.currentTime||0).toFixed(1), muted: a.muted, volume: a.volume, paused: a.paused, err: window.__probeErr || null }; })()`)));
  }

  console.log('\n=== PHASE 2: DRM track via MusicKit — LISTEN NOW ===');
  await ev(`(() => {
    const mk = window.MusicKit.getInstance();
    window.__r2 = { queued: false };
    mk.setQueue({ song: ${JSON.stringify(SONG)} }).then(() => { window.__r2.queued = true; }).catch((e) => { window.__r2.error = String(e); });
    return true;
  })()`);
  for (let i = 0; i < 20 && !(await ev(`!!(window.__r2 && window.__r2.queued)`)); i++) await sleep(1000);
  await ev(`window.MusicKit.getInstance().play()`);
  for (let i = 0; i < 8; i++) {
    await sleep(2500);
    console.log('  ' + JSON.stringify(await ev(`(() => {
      const mk = window.MusicKit.getInstance();
      const els = [...document.querySelectorAll('audio,video')].map((el) => ({ tag: el.tagName, muted: el.muted, volume: el.volume, t: +(el.currentTime||0).toFixed(1), src: (el.src||'').slice(0, 24) }));
      return { mkVolume: mk.volume, playing: mk.isPlaying, t: +(mk.currentPlaybackTime||0).toFixed(1), d: +(mk.currentPlaybackDuration||0).toFixed(1), elements: els };
    })()`)));
  }

  // What does the browser think its audio output is?
  console.log('\n=== audio output info ===');
  console.log('  ' + JSON.stringify(await ev(`(async () => {
    const ctx = new AudioContext();
    const state = ctx.state;
    const sr = ctx.sampleRate;
    const latency = typeof ctx.outputLatency === 'number' ? ctx.outputLatency : null;
    try { await ctx.resume(); } catch {}
    const after = ctx.state;
    return { state, afterResume: after, sampleRate: sr, outputLatency: latency, baseLatency: ctx.baseLatency };
  })()`)));
  ws.close();
} catch (e) {
  console.log('ERROR:', e.message);
} finally {
  try { spawnSync('taskkill', ['/PID', String(chrome.pid), '/T', '/F'], { stdio: 'ignore' }); } catch {}
}
process.exit(0);
