// Diagnostic: list a library playlist's tracks and check which catalogue ids
// actually resolve. Prints no secrets.
import fs from 'node:fs';
import path from 'node:path';

const cfgPath = path.join(process.env.APPDATA, 'applemusic-cli', 'config.json');
const cfg = JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
const sf = cfg.storefront || 'us';

const headers = {
  Authorization: 'Bearer ' + cfg.devToken,
  'Music-User-Token': cfg.musicUserToken,
  Origin: 'https://music.apple.com',
  Referer: 'https://music.apple.com/',
  'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36',
};

async function get(url) {
  const res = await fetch(url, { headers });
  if (!res.ok) throw new Error(res.status + ' ' + url + ' :: ' + (await res.text()).slice(0, 200));
  return res.json();
}

// Find the playlist by name.
const lists = await get('https://api.music.apple.com/v1/me/library/playlists?limit=100');
const target = lists.data.find((p) => (p.attributes?.name || '').toLowerCase() === 'favourite songs')
  || lists.data[0];
console.log('playlist:', target.attributes?.name, 'id=', target.id);

const tracks = [];
let next = `https://api.music.apple.com/v1/me/library/playlists/${target.id}/tracks?limit=100`;
while (next) {
  const page = await get(next);
  tracks.push(...page.data);
  next = page.next ? 'https://api.music.apple.com' + page.next : null;
}

console.log('tracks:', tracks.length);
const catalogIds = [];
for (const t of tracks) {
  const a = t.attributes || {};
  const pp = a.playParams || {};
  const catalogId = pp.catalogId || '';
  console.log(`${t.id}  catalog=${catalogId || '(none)'}  preview=${a.previews?.length ? 'yes' : 'no'}  ${a.name} — ${a.artistName}`);
  if (catalogId) catalogIds.push(catalogId);
  else catalogIds.push(t.id); // what our CLI would fall back to
}

// Which of those resolve in the catalogue?
const resolved = new Set();
for (let i = 0; i < catalogIds.length; i += 100) {
  const batch = catalogIds.slice(i, i + 100);
  const res = await fetch(`https://api.music.apple.com/v1/catalog/${sf}/songs?ids=${batch.join(',')}`, { headers });
  const body = await res.json().catch(() => ({}));
  for (const item of body.data || []) resolved.add(item.id);
  for (const err of body.errors || []) console.log('catalog error:', JSON.stringify(err));
}
console.log('\nunresolved ids:', catalogIds.filter((id) => !resolved.has(id)).join(', ') || '(none)');
