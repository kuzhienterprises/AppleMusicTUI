// Spike 16: does MusicKit accept library song ids (i.*) in setQueue? If so,
// library playback avoids storefront/catalogue mismatches entirely.
import { spawn, spawnSync } from 'node:child_process';
import path from 'node:path';

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const PROFILE = path.join(process.env.APPDATA, 'applemusic-cli', 'browser-profile');
const PORT = 9560;

const LIBRARY_IDS = [
  'i.RBrx22LSzdMbJ6E', 'i.JL1aJVrTzB7x5qO', 'i.4YBNkbxSXbmPNr3',
  'i.GEGNoVJIQJrZGkp', 'i.b1JE2p0TaO6ZP4g', 'i.eoD82L7SMe9o8KL',
  'i.mmpeOrZiLWrJM20',
];

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
setTimeout(() => { console.log('WATCHDOG'); process.exit(3); }, 180000).unref?.();

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
  console.log('MusicKit ready');

  await ev(`(() => {
    const mk = window.MusicKit.getInstance();
    window.__r = { queued: false, error: null };
    mk.setQueue({ songs: ${JSON.stringify(LIBRARY_IDS)} })
      .then(() => { window.__r.queued = true; })
      .catch((e) => { window.__r.error = String((e && e.message) || e); });
    return true;
  })()`);
  await sleep(8000);
  console.log('queue result:', JSON.stringify(await ev(`(() => {
    const mk = window.MusicKit.getInstance();
    return {
      state: window.__r,
      length: (mk.queue && mk.queue.length) || 0,
      pos: (mk.queue && mk.queue.position),
      item: mk.nowPlayingItem ? { id: mk.nowPlayingItem.id, name: mk.nowPlayingItem.attributes && mk.nowPlayingItem.attributes.name } : null,
      duration: mk.currentPlaybackDuration || 0,
    };
  })()`)));

  if (await ev(`!!(window.__r && window.__r.queued)`)) {
    await ev(`window.MusicKit.getInstance().play()`);
    await sleep(5000);
    console.log('after play:', JSON.stringify(await ev(`(() => {
      const mk = window.MusicKit.getInstance();
      return { playing: mk.isPlaying, t: +(mk.currentPlaybackTime || 0).toFixed(1), d: +(mk.currentPlaybackDuration || 0).toFixed(1), pos: mk.queue.position };
    })()`)));
    await ev(`window.MusicKit.getInstance().skipToNextItem()`);
    await sleep(4000);
    console.log('after next:', JSON.stringify(await ev(`(() => {
      const mk = window.MusicKit.getInstance();
      return { playing: mk.isPlaying, t: +(mk.currentPlaybackTime || 0).toFixed(1), d: +(mk.currentPlaybackDuration || 0).toFixed(1), pos: mk.queue.position, name: mk.nowPlayingItem && mk.nowPlayingItem.attributes && mk.nowPlayingItem.attributes.name };
    })()`)));
  }
  ws.close();
} catch (e) {
  console.log('ERROR:', e.message);
} finally {
  try { spawnSync('taskkill', ['/PID', String(chrome.pid), '/T', '/F'], { stdio: 'ignore' }); } catch {}
}
process.exit(0);
