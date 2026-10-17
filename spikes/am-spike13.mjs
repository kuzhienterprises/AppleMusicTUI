// Spike 13: production-shaped playback prototype (minimal flags).
// node am-spike13.mjs [--login-profile] [--offscreen] [--port=NNNN] [--no-token]
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const CONFIG = path.join(process.env.APPDATA, 'applemusic-cli', 'config.json');
const LOGIN_PROFILE = path.join(process.env.APPDATA, 'applemusic-cli', 'browser-profile');
const PAGE = 'https://music.apple.com/in/browse';
const SONG_ID = '1097861836'; // Karma Police — 264s

const getArg = (n) => process.argv.find((a) => a.startsWith(`--${n}=`))?.slice(n.length + 3);
const WSIZE = getArg('wsize') || '900,700';
const WPOS = getArg('wpos') || '0,0';
const USE_LOGIN = process.argv.includes('--login-profile');
const OFFS = process.argv.includes('--offscreen');
const NO_TOKEN = process.argv.includes('--no-token');
const PORT = Number(getArg('port') || 9460);

const cfg = JSON.parse(fs.readFileSync(CONFIG, 'utf8'));
const profile = USE_LOGIN ? LOGIN_PROFILE : fs.mkdtempSync(path.join(os.tmpdir(), 'am-p13-'));

const args = [
  `--user-data-dir=${profile}`,
  `--remote-debugging-port=${PORT}`,
  '--remote-allow-origins=*',
  '--no-first-run',
  '--no-default-browser-check',
  '--autoplay-policy=no-user-gesture-required',
];
if (process.argv.includes('--nothrottle')) {
  args.push(
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
    '--disable-background-timer-throttling',
    '--disable-features=CalculateNativeWinOcclusion',
  );
}
if (OFFS) {
  args.push(`--window-size=${WSIZE}`, '--window-position=-32000,-32000');
} else args.push(`--window-size=${WSIZE}`, `--window-position=${WPOS}`);
args.push(PAGE);

console.log(`profile=${USE_LOGIN ? 'login' : 'temp(' + profile + ')'} offscreen=${OFFS} port=${PORT}`);
const chrome = spawn(CHROME, args, { stdio: ['ignore', 'ignore', 'pipe'] });
chrome.stderr.on('data', (d) => { const s = String(d).trim(); if (s && /DevTools|FATAL|crash server/i.test(s)) console.log('  chrome:', s.split('\n')[0].slice(0, 130)); });
chrome.on('exit', (c) => console.log('  chrome exited:', c));

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
setTimeout(() => { console.log('WATCHDOG'); process.exit(3); }, 200000).unref?.();

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
  let id = 1;
  const pending = new Map();
  const media = [];
  ws.addEventListener('message', (ev) => {
    let m; try { m = JSON.parse(ev.data); } catch { return; }
    if (m.id && pending.has(m.id)) { const r = pending.get(m.id); pending.delete(m.id); r(m); return; }
    if (m.method === 'Network.requestWillBeSent' && /widevine|acquireWebPlaybackLicense|\/Music22|\/AudioPreview/i.test(m.params.request.url)) {
      media.push(m.params.request.url.replace(/^https:\/\//, '').split('?')[0].slice(0, 70));
    }
  });
  const call = (method, params = {}) => new Promise((res, rej) => {
    const myId = id++;
    const t = setTimeout(() => { pending.delete(myId); rej(new Error(`${method} timeout`)); }, 15000);
    pending.set(myId, (m) => { clearTimeout(t); m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m.result); });
    ws.send(JSON.stringify({ id: myId, method, params }));
  });
  const evaluate = async (expression) => {
    const r = await call('Runtime.evaluate', { expression, returnByValue: true });
    if (r.exceptionDetails) return { __error: r.exceptionDetails.exception?.description || r.exceptionDetails.text };
    return r.result.value;
  };

  await call('Runtime.enable'); await call('Network.enable');
  const skipCookie = process.argv.includes('--nocookie');
  if (!skipCookie) {
    for (const domain of ['.music.apple.com', 'music.apple.com', '.apple.com']) {
      await call('Network.setCookie', { name: 'media-user-token', value: cfg.musicUserToken, domain, path: '/', secure: true, httpOnly: true }).catch(() => {});
    }
    await call('Page.reload');
  }
  console.log('booting…');
  await sleep(14000);

  // Keep the page un-throttled: focus it and raise the window.
  await call('Page.bringToFront').catch(() => {});
  await call('Emulation.setFocusEmulationEnabled', { enabled: true }).catch(() => {});

  const kicked = await evaluate(`(() => {
    window.__log = [];
    const mk = window.MusicKit && window.MusicKit.getInstance ? window.MusicKit.getInstance() : null;
    if (!mk) { window.__log.push('no MusicKit'); return { ok: false }; }
    ${NO_TOKEN ? '' : `try { mk.musicUserToken = ${JSON.stringify(cfg.musicUserToken)}; window.__log.push('token set; auth=' + mk.authorizationStatus); } catch (e) { window.__log.push('token fail: ' + e.message); }`}
    mk.setQueue({ song: ${JSON.stringify(SONG_ID)} })
      .then((q) => { window.__log.push('queue len=' + (q && q.length)); return mk.play(); })
      .then(() => window.__log.push('play ok'))
      .catch((e) => window.__log.push('ERR ' + (e && e.message)));
    return { ok: true };
  })()`);
  console.log('kick:', JSON.stringify(kicked));

  let bestTime = 0, bestDur = 0, sawPlaying = false;
  for (let i = 0; i < 22; i++) {
    await sleep(1500);
    const s = await evaluate(`(() => {
      const mk = window.MusicKit && window.MusicKit.getInstance ? window.MusicKit.getInstance() : null;
      const a = document.querySelector('audio');
      return {
        log: window.__log || [],
        mkTime: mk ? +mk.currentPlaybackTime.toFixed(2) : null,
        mkPlaying: mk ? mk.isPlaying : null,
        dur: a ? +(a.duration || 0).toFixed(1) : null,
        src: a && a.src ? a.src.slice(0, 26) : null,
      };
    })()`);
    if (s?.mkTime > bestTime) bestTime = s.mkTime;
    if (s?.dur > bestDur) bestDur = s.dur;
    if (s?.mkPlaying) sawPlaying = true;
    if (i % 3 === 0) console.log(`t+${i}: time=${s?.mkTime} dur=${s?.dur} playing=${s?.mkPlaying} log=${JSON.stringify(s?.log)}`);
    if (bestDur > 200 && bestTime > 8) break;
  }
  console.log('---');
  console.log(`duration=${bestDur} (264=full) time=${bestTime} everPlaying=${sawPlaying}`);
  console.log('media:', JSON.stringify([...new Set(media)].slice(0, 6)));
  console.log('VERDICT:', bestDur > 200 && bestTime > 4 ? 'FULL DRM TRACK PLAYING' : sawPlaying ? 'playing, not full track' : 'NOT PLAYING');
  ws.close();
} catch (e) {
  console.log('ERROR:', e.message);
} finally {
  try { spawnSync('taskkill', ['/PID', String(chrome.pid), '/T', '/F'], { stdio: 'ignore' }); } catch {}
  if (!USE_LOGIN) { try { fs.rmSync(profile, { recursive: true, force: true }); } catch {} }
}
process.exit(0);
