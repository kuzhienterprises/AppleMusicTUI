// Spike 14: can Chrome play ANY audio right now? Plain public MP3, no Apple,
// no DRM. If this stalls, the problem is the audio output device, not Widevine.
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const LOGIN_PROFILE = path.join(process.env.APPDATA, 'applemusic-cli', 'browser-profile');
const USE_LOGIN = process.argv.includes('--login-profile');
const PORT = 9500;
const MP3 = process.argv.find((a) => a.startsWith('--mp3='))?.slice(6) || 'https://download.samplelib.com/mp3/sample-15s.mp3';

const profile = USE_LOGIN ? LOGIN_PROFILE : fs.mkdtempSync(path.join(os.tmpdir(), 'am-p14-'));
const chrome = spawn(CHROME, [
  `--user-data-dir=${profile}`,
  `--remote-debugging-port=${PORT}`,
  '--remote-allow-origins=*',
  '--no-first-run', '--no-default-browser-check',
  '--autoplay-policy=no-user-gesture-required',
  '--window-size=900,700', '--window-position=0,0',
  'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] });

const errs = [];
chrome.stderr.on('data', (d) => { const s = String(d).trim(); if (s && !/DevTools listening/.test(s)) errs.push(...s.split('\n').slice(0, 30)); });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
setTimeout(() => { console.log('WATCHDOG'); process.exit(3); }, 150000).unref?.();

try {
  const deadline = Date.now() + 60000;
  let up = false;
  while (Date.now() < deadline) { try { if ((await fetch(`http://127.0.0.1:${PORT}/json/version`)).ok) { up = true; break; } } catch {} await sleep(500); }
  if (!up) throw new Error('debugger not up');

  const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
  const page = list.find((t) => t.type === 'page');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', () => rej(new Error('ws'))); });
  let id = 1; const pending = new Map();
  ws.addEventListener('message', (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } });
  const call = (method, params = {}) => new Promise((res, rej) => {
    const myId = id++;
    const t = setTimeout(() => { pending.delete(myId); rej(new Error(method + ' timeout')); }, 15000);
    pending.set(myId, (m) => { clearTimeout(t); m.error ? rej(new Error(m.error.message)) : res(m.result); });
    ws.send(JSON.stringify({ id: myId, method, params }));
  });
  const evaluate = async (e) => {
    const r = await call('Runtime.evaluate', { expression: e, returnByValue: true });
    return r.exceptionDetails ? { __error: r.exceptionDetails.exception?.description || r.exceptionDetails.text } : r.result.value;
  };

  await call('Runtime.enable');
  console.log('mp3:', MP3);
  const start = await evaluate(`(() => {
    const a = document.createElement('audio');
    a.src = ${JSON.stringify(MP3)};
    a.volume = 1;
    document.body.appendChild(a);
    window.__a = a;
    a.play().then(() => { window.__played = true; }).catch((e) => { window.__playErr = e.name + ': ' + e.message; });
    return { ok: true };
  })()`);
  console.log('start:', JSON.stringify(start));

  for (let i = 0; i < 16; i++) {
    await sleep(1000);
    const s = await evaluate(`(() => {
      const a = window.__a;
      return {
        t: +a.currentTime.toFixed(2), dur: +(a.duration || 0).toFixed(1), paused: a.paused,
        rs: a.readyState, ns: a.networkState, err: a.error ? a.error.code + ':' + a.error.message : null,
        played: !!window.__played, playErr: window.__playErr || null,
      };
    })()`);
    console.log(`t+${i}:`, JSON.stringify(s));
    if (s?.t > 5) break;
  }
  console.log('--- chrome stderr (audio-related) ---');
  console.log(errs.filter((l) => /audio|sink|WASAPI|media|shared-memory|Corruption/i.test(l)).slice(0, 12).join('\n') || '(none)');
  ws.close();
} catch (e) {
  console.log('ERROR:', e.message);
} finally {
  try { spawnSync('taskkill', ['/PID', String(chrome.pid), '/T', '/F'], { stdio: 'ignore' }); } catch {}
  if (!USE_LOGIN) { try { fs.rmSync(profile, { recursive: true, force: true }); } catch {} }
}
process.exit(0);
