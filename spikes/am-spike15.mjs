// Spike 15: validate the exact MusicKit control surface the Go engine will use.
// Queue an album, start at a song, then exercise skip/seek/volume/pause.
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const LOGIN_PROFILE = path.join(process.env.APPDATA, 'applemusic-cli', 'browser-profile');
const PORT = 9550;
const ALBUM = '1097861387';       // OK Computer
const START_SONG = '1097861836';  // Karma Police

const profile = LOGIN_PROFILE;
const args = [
  `--user-data-dir=${profile}`,
  `--remote-debugging-port=${PORT}`,
  '--remote-allow-origins=*',
  '--no-first-run', '--no-default-browser-check',
  '--autoplay-policy=no-user-gesture-required',
  '--disable-features=Translate,MediaRouter',
  '--window-size=400,300', '--window-position=-32000,-32000',
  'https://music.apple.com/in/browse',
];
const chrome = spawn(CHROME, args, { stdio: ['ignore', 'ignore', 'pipe'] });
chrome.stderr.on('data', () => {});
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
  let id = 1; const pending = new Map();
  ws.addEventListener('message', (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } });
  const call = (method, params = {}) => new Promise((res, rej) => {
    const myId = id++;
    const t = setTimeout(() => { pending.delete(myId); rej(new Error(method + ' timeout')); }, 20000);
    pending.set(myId, (m) => { clearTimeout(t); m.error ? rej(new Error(m.error.message)) : res(m.result); });
    ws.send(JSON.stringify({ id: myId, method, params }));
  });
  const ev = async (expr) => {
    const r = await call('Runtime.evaluate', { expression: expr, returnByValue: true });
    if (r.exceptionDetails) return { __error: r.exceptionDetails.exception?.description || r.exceptionDetails.text };
    return r.result.value;
  };

  await call('Runtime.enable');
  // Wait until the web player has configured MusicKit.
  await waitFor(async () => await ev(`typeof window.MusicKit !== 'undefined' && !!window.MusicKit.getInstance()`), 60000, 'MusicKit');
  await call('Page.bringToFront').catch(() => {});
  await call('Emulation.setFocusEmulationEnabled', { enabled: true }).catch(() => {});
  console.log('MusicKit ready');

  // 1. Queue the album starting at our chosen song (fire and forget, then poll).
  await ev(`(() => {
    const mk = window.MusicKit.getInstance();
    window.__q = { steps: [] };
    mk.setQueue({ album: ${JSON.stringify(ALBUM)}, startWithSong: ${JSON.stringify(START_SONG)}, startPlaying: true })
      .then(() => { window.__q.done = true; window.__q.pos = mk.queue ? mk.queue.position : null; window.__q.len = mk.queue ? mk.queue.length : null; })
      .catch((e) => { window.__q.err = String(e && e.message); });
    return 'queued';
  })()`);
  await sleep(6000);
  const afterQueue = await ev(`(() => { const mk = window.MusicKit.getInstance(); return { q: window.__q, pos: mk.queue ? mk.queue.position : null, len: mk.queue ? mk.queue.length : null, d: +mk.currentPlaybackDuration.toFixed(1), t: +mk.currentPlaybackTime.toFixed(1) }; })()`);
  console.log('after setQueue:', JSON.stringify(afterQueue));

  // 2. Read the current item.
  const item = await ev(`(() => { const mk = window.MusicKit.getInstance(); const i = mk.nowPlayingItem; return i ? { id: i.id, name: i.attributes && i.attributes.name, artist: i.attributes && i.attributes.artistName } : null; })()`);
  console.log('nowPlayingItem:', JSON.stringify(item));

  // 3. Seek.
  await ev(`window.MusicKit.getInstance().seekToTime(30)`);
  await sleep(1500);
  console.log('after seek(30):', JSON.stringify(await ev(`({ t: +window.MusicKit.getInstance().currentPlaybackTime.toFixed(1), d: +window.MusicKit.getInstance().currentPlaybackDuration.toFixed(1) })`)));

  // 4. Volume.
  await ev(`window.MusicKit.getInstance().volume = 0.4`);
  await sleep(300);
  console.log('after volume=0.4:', JSON.stringify(await ev(`({ v: window.MusicKit.getInstance().volume })`)));

  // 5. Pause / resume.
  await ev(`window.MusicKit.getInstance().pause()`);
  await sleep(800);
  console.log('after pause:', JSON.stringify(await ev(`({ playing: window.MusicKit.getInstance().isPlaying })`)));
  await ev(`window.MusicKit.getInstance().play()`);
  await sleep(800);
  console.log('after play:', JSON.stringify(await ev(`({ playing: window.MusicKit.getInstance().isPlaying })`)));

  // 6. Skip forward and back.
  await ev(`window.MusicKit.getInstance().skipToNextItem()`);
  await sleep(2500);
  console.log('after next:', JSON.stringify(await ev(`(() => { const mk = window.MusicKit.getInstance(); const i = mk.nowPlayingItem; return { pos: mk.queue.position, id: i && i.id, name: i && i.attributes && i.attributes.name, d: +mk.currentPlaybackDuration.toFixed(1) }; })()`)));
  await ev(`window.MusicKit.getInstance().skipToPreviousItem()`);
  await sleep(2500);
  console.log('after prev:', JSON.stringify(await ev(`(() => { const mk = window.MusicKit.getInstance(); const i = mk.nowPlayingItem; return { pos: mk.queue.position, id: i && i.id, name: i && i.attributes && i.attributes.name }; })()`)));

  console.log('playbackState:', JSON.stringify(await ev(`window.MusicKit.getInstance().playbackState`)));
  ws.close();
} catch (e) {
  console.log('ERROR:', e.message);
} finally {
  try { spawnSync('taskkill', ['/PID', String(chrome.pid), '/T', '/F'], { stdio: 'ignore' }); } catch {}
}
process.exit(0);
