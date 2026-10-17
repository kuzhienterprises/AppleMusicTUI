# Contributing

Thanks for taking a look. Bug reports, playback fixes and platform patches are
all welcome — the interfaces are small on purpose.

## Getting set up

Go 1.26 or newer, and a terminal that can do truecolor. Then:

```sh
git clone https://github.com/kuzhienterprises/AppleMusicTUI
cd AppleMusicTUI
go build ./...
go test ./...
```

To run what you just built:

```sh
go run ./cmd/am status     # what the app thinks its environment is
go run ./cmd/am playlists  # the interface
```

If you want to poke at playback without a terminal taking over, `--plain` prints
instead of drawing:

```sh
go run ./cmd/am --plain search "instant crush"
go run ./cmd/am --verbose play "instant crush"
```

## Where things live

| Package | What it does |
| --- | --- |
| `internal/apple` | The two HTTP surfaces: the catalogue API and the library API, plus the developer token that both need. |
| `internal/auth` | Browser sign-in over the Chrome DevTools Protocol, and the session it leaves behind. |
| `internal/cdp` | A small CDP client. Nothing here knows what Apple Music is. |
| `internal/player` | The `Engine` interface, the hidden-browser engine that plays encrypted audio, and the mpv engine that plays previews. |
| `internal/visualizer` | WASAPI loopback capture and the FFT that feeds the spectrum. |
| `internal/tui` | The interface: layout, styling, key handling. |
| `internal/cmd` | Command wiring. Thin — the work is in the packages above. |
| `spikes/` | One-off harnesses used while working on the awkward parts. Not shipped. |

`internal/cmd/am` is the entry point; the binary it produces is `am`.

## Things worth knowing before you change playback

Two platform-specific pairs carry the odd corners:

- `internal/player/pipe_windows.go` / `pipe_other.go`
- `internal/visualizer/loopback_windows.go` / `loopback_other.go`

Full-length audio is DRM encrypted. It is played by Apple's own web player in a
browser that is deliberately moved off-screen, and the terminal drives it
through MusicKit. A few details there are load-bearing, and each of them cost
time to find:

- The browser is started with `--autoplay-policy=no-user-gesture-required`.
  Without it the first track silently never starts.
- The page is activated with `Page.bringToFront` plus
  `Emulation.setFocusEmulationEnabled`. Without it MusicKit considers itself in
  the background and throttles playback.
- **Do not inject the `media-user-token` cookie.** It looks like the missing
  piece, and it breaks playback: the engine silently falls back to a 90 second
  preview. `Status.PreviewFallback` exists to make that regression visible.
- Library tracks have to be queued by library id (`i.…`), not catalogue id, or
  MusicKit answers `[mk-007] NOT_FOUND`.
- `queue.position` is `-1` until an item is actually selected, so it is not a
  "playing" signal.

## Screenshots and the demo

The images in the README are rendered from the interface itself, so they cannot
drift away from it. The harness writes frames, and a small spike turns them into
a page that gets screenshotted:

```sh
# frames, as ANSI text (needs an artwork URL to use as the cover)
AM_SHOT=/tmp/frames AM_SHOT_ART='https://…/{w}x{h}bb.jpg' \
  go test ./internal/tui -run TestShotFrames

# frames → images
go run ./spikes/shots -out /tmp/shots -font /path/CascadiaMono.ttf /tmp/frames/*.ansi
```

`AM_SHOT_MOTION=1` also writes the frames of the spectrum animation.

The demo clip is a real session, recorded with [vhs](https://github.com/charmbracelet/vhs):

```sh
vhs docs/demo.tape
```

It plays music while it records. The clip is silent, but the spectrum in it is
not simulated — it is the system's audio read back through loopback capture.

Two things make that re-recordable. vhs renders the terminal in a browser of its
own, and the hidden player is a second Chrome booting on the same machine, so a
cold boot can lose the race against the loading deadline — 43 seconds cold
against 24 warm, measured here. Prime the profile first, and give the take room:

```sh
am --plain play "instant crush"          # warm the profile, then ^C
AM_BROWSER_TIMEOUT=5m vhs docs/demo.tape # and nothing else running
```

`AM_BROWSER_TIMEOUT` is the same knob the README offers users on slow machines;
it only raises how long the player waits for Apple's page to boot.

## Style

- `gofmt`, and `go vet` clean.
- Comments explain *why*. The code is not complicated; the reasons behind some
  of it are.
- New behaviour comes with a test. The layout tests in `internal/tui` are the
  ones to copy: they assert on what is on the screen at a given size.
- Keep platform-specific code behind the file-name suffixes rather than build
  tags where you can, with a stub that says what it is missing.

## Pull requests

Small and focused beats large and tidy. If a change touches playback, say in the
description what you heard and on what platform — that is the part CI cannot
check.
