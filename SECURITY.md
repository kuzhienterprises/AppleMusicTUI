# Security

## Reporting a problem

Please report vulnerabilities privately, through GitHub's
[security advisory](https://github.com/kuzhienterprises/AppleMusicTUI/security/advisories/new)
form rather than in a public issue. A short description of what an attacker
gains is more useful than a long one; if playback or a token is involved, say
which platform you saw it on.

Only the latest release is supported. This is a small project with no LTS
branch.

## What is on your disk

`am` keeps two things, both outside the repository:

| Path | Contents |
| --- | --- |
| `%APPDATA%\applemusic-cli\config.json` | The scraped developer token, the captured music user token, the storefront and any overrides. Written with owner-only permissions (`0600`, directory `0700`) |
| `%APPDATA%\applemusic-cli\browser-profile\` | A Chromium profile used only for Apple Music sign-in, so your everyday browsing profile is never touched |

On macOS and Linux both live under `$XDG_CONFIG_HOME/applemusic-cli`, or
`~/.config/applemusic-cli`.

**The music user token is a bearer credential.** Anyone who can read that file
can use your Apple Music account. Treat `config.json` like a password: do not
commit it, do not paste it into an issue, and if it leaks run `am logout --purge`
and sign in again. `--purge` deletes the profile and developer token as well;
plain `am logout` only forgets the session.

## Where it talks to

- `music.apple.com` and `*.mzstatic.com` — the web player, its assets, and cover
  art.
- `amp-api.music.apple.com` — the catalogue and library APIs.
- Nothing else. There is no telemetry, no analytics, and no update check: the
  binary makes no outbound request other than the ones above.

The browser it starts is a normal Chrome or Edge instance pointed at
`music.apple.com`, with remote debugging enabled on a local port for the lifetime
of the process. It is launched with its own `--user-data-dir`, so it shares no
cookies, history or extensions with your own browser.

## What it does not do

It does not decrypt audio or extract DRM keys. Full-length playback is delegated
to Apple's own web player inside that browser, which negotiates Widevine exactly
as it does when you press play on the site. If you are looking for something that
strips Apple's DRM, this is not it and will not become it.
