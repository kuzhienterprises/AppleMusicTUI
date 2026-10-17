#!/bin/sh
# am installer for macOS and Linux.
#
# Downloads the release built for this machine, verifies it against the
# checksums.txt published with that release, installs it into ~/.local/bin,
# adds that directory to your PATH, and runs am doctor at the end.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kuzhienterprises/AppleMusicTUI/main/install.sh | sh
#   sh install.sh --version v0.1.1 --dir /usr/local/bin
#   sh install.sh --uninstall
#
# Read it before you pipe it: it is short, and it only ever touches the
# directory you install into, your shell profile, and a temporary directory.

set -eu

REPO='kuzhienterprises/AppleMusicTUI'
VERSION='latest'
DIR="${HOME}/.local/bin"
SHARE="${XDG_DATA_HOME:-$HOME/.local/share}/am"
MODIFY_PATH=1
WITH_MPV=0
UNINSTALL=0
DRY_RUN=0

say()  { printf '==> %s\n' "$1"; }
ok()   { printf '  ok  %s\n' "$1"; }
warn() { printf '  !!  %s\n' "$1"; }
die()  { printf '  xx  %s\n' "$1" >&2; exit 1; }

usage() {
    cat <<'EOF'
am installer

  --version TAG      release to install, e.g. v0.1.1 (default: latest)
  --dir PATH         where to put the binary (default: ~/.local/bin)
  --no-modify-path   do not touch your shell profile
  --with-mpv         install mpv with your package manager, if possible
  --uninstall        remove am and the files the installer added
  --dry-run          print what would happen, change nothing
  --help             this text

The release is fetched from GitHub. Set AM_INSTALL_BASE_URL to use a mirror
instead, with --version given explicitly.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        --dir) DIR="$2"; shift 2 ;;
        --dir=*) DIR="${1#*=}"; shift ;;
        --no-modify-path) MODIFY_PATH=0; shift ;;
        --with-mpv) WITH_MPV=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

if [ -n "${AM_INSTALL_BASE_URL:-}" ] && [ "$VERSION" = 'latest' ]; then
    die 'set --version when AM_INSTALL_BASE_URL is used, e.g. --version v0.1.1'
fi

# ---------------------------------------------------------------- platform

os=$(uname -s)
arch=$(uname -m)

case "$os" in
    Darwin) goos=darwin ;;
    Linux)  goos=linux ;;
    *)
        die "there is no prebuilt binary for $os; build from source with:
  go install github.com/${REPO}/cmd/am@latest"
        ;;
esac

case "$arch" in
    x86_64|amd64)  goarch=amd64 ;;
    arm64|aarch64) goarch=arm64 ;;
    *) die "there is no prebuilt binary for $arch; build from source with:
  go install github.com/${REPO}/cmd/am@latest" ;;
esac

# ---------------------------------------------------------------- fetching

if command -v curl >/dev/null 2>&1; then
    fetch()    { curl -fsSL "$1"; }
    download() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch()    { wget -q -O - "$1"; }
    download() { wget -q -O "$2" "$1"; }
else
    die 'curl or wget is required'
fi

if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
    die 'sha256sum or shasum is required to verify the download'
fi

# ---------------------------------------------------------------- uninstall

if [ "$UNINSTALL" -eq 1 ]; then
    say 'Removing am'
    if [ -f "${DIR}/am" ]; then
        rm -f "${DIR}/am"
        ok "removed ${DIR}/am"
    else
        warn "there is no am at ${DIR}/am"
    fi
    if [ -d "$SHARE" ]; then
        rm -rf "$SHARE"
        ok "removed $SHARE"
    fi
    if [ "$MODIFY_PATH" -eq 1 ]; then
        warn 'any PATH line the installer added to your shell profile is still there'
        warn "remove the line commented '# am: Apple Music terminal client' yourself"
    fi
    printf '\n'
    ok 'your settings and sign-in were left alone'
    printf '  %s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/applemusic-cli"
    printf '\n'
    exit 0
fi

# ---------------------------------------------------------------- resolve

if [ "$VERSION" = 'latest' ]; then
    say 'Finding the latest release'
    tag=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
        | head -n 1)
    [ -n "$tag" ] || die 'could not read the latest release from the GitHub API; pass --version v0.1.1'
else
    tag=$VERSION
    case "$tag" in v*) ;; *) tag="v$tag" ;; esac
fi
ver=${tag#v}

if [ -n "${AM_INSTALL_BASE_URL:-}" ]; then
    base=${AM_INSTALL_BASE_URL%/}
else
    base="https://github.com/${REPO}/releases/download/${tag}"
fi

pkg="am_${ver}_${goos}_${goarch}.tar.gz"

if [ "$DRY_RUN" -eq 1 ]; then
    say 'Dry run, nothing will be changed'
    printf '  release    %s\n' "$tag"
    printf '  asset      %s\n' "$pkg"
    printf '  from       %s\n' "$base"
    printf '  install to %s/am\n' "$DIR"
    if [ "$MODIFY_PATH" -eq 1 ]; then
        printf '  PATH       add %s to your shell profile\n' "$DIR"
    else
        printf '  PATH       left alone\n'
    fi
    printf '\n'
    exit 0
fi

# ---------------------------------------------------------------- download

tmp=$(mktemp -d "${TMPDIR:-/tmp}/am-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $pkg ($tag)"
download "${base}/${pkg}" "${tmp}/${pkg}" || die "download failed: ${base}/${pkg}"
download "${base}/checksums.txt" "${tmp}/checksums.txt" || die 'could not fetch checksums.txt'

say 'Verifying the download'
want=$(awk -v f="$pkg" '$2 == f { print $1 }' "${tmp}/checksums.txt" | head -n 1)
[ -n "$want" ] || die "checksums.txt has no entry for $pkg"
got=$(sha256 "${tmp}/${pkg}")
[ "$got" = "$want" ] || die "checksum mismatch for $pkg
  expected $want
  got      $got"
ok "sha256 $(printf '%s' "$got" | cut -c 1-16)..."

say 'Installing'
tar -xzf "${tmp}/${pkg}" -C "$tmp" || die "could not extract $pkg"
[ -f "${tmp}/am" ] || die 'the archive did not contain am'
mkdir -p "$DIR"
cp -f "${tmp}/am" "${DIR}/am"
chmod 755 "${DIR}/am"
# The licence travels with the binary.
mkdir -p "$SHARE"
if [ -f "${tmp}/LICENSE" ]; then cp -f "${tmp}/LICENSE" "${SHARE}/LICENSE"; fi
if [ -f "${tmp}/README.md" ]; then cp -f "${tmp}/README.md" "${SHARE}/README.md"; fi
ok "installed ${DIR}/am"

# ---------------------------------------------------------------- PATH

case ":${PATH}:" in
    *":${DIR}:"*) on_path=1 ;;
    *) on_path=0 ;;
esac

if [ "$on_path" -eq 1 ]; then
    ok "already on your PATH: $DIR"
elif [ "$MODIFY_PATH" -eq 0 ]; then
    warn "not on your PATH, and --no-modify-path was given"
    warn "  add it yourself: export PATH=\"${DIR}:\$PATH\""
else
    shellname=$(basename "${SHELL:-sh}")
    case "$shellname" in
        zsh)  rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
        bash)
            if [ "$goos" = 'darwin' ]; then rc="${HOME}/.bash_profile"; else rc="${HOME}/.bashrc"; fi
            ;;
        *)    rc="${HOME}/.profile" ;;
    esac
    if [ -f "$rc" ] && grep -q '^# am: Apple Music terminal client' "$rc" 2>/dev/null; then
        ok "PATH already set up in $rc"
    else
        {
            printf '\n# am: Apple Music terminal client\n'
            printf 'export PATH="%s:$PATH"\n' "$DIR"
        } >> "$rc"
        ok "added $DIR to your PATH in $rc"
    fi
fi

# ---------------------------------------------------------------- checks

say 'Checking this machine'
"${DIR}/am" --version || warn 'the binary did not run'

have_browser=0
if [ "$goos" = 'darwin' ]; then
    for app in '/Applications/Google Chrome.app' '/Applications/Microsoft Edge.app' '/Applications/Chromium.app' '/Applications/Brave Browser.app'; do
        if [ -d "$app" ]; then have_browser=1; break; fi
    done
else
    for exe in google-chrome google-chrome-stable chromium chromium-browser microsoft-edge brave-browser; do
        if command -v "$exe" >/dev/null 2>&1; then have_browser=1; break; fi
    done
fi

if [ "$have_browser" -eq 1 ]; then
    ok 'a Chromium browser is available'
else
    warn 'no Chrome, Edge or Chromium found: one is needed for sign-in and full-length playback'
fi

if command -v mpv >/dev/null 2>&1; then
    ok 'mpv is available'
else
    warn 'mpv is not installed: it is only needed for 30 second previews'
    if [ "$goos" = 'darwin' ]; then
        if [ "$WITH_MPV" -eq 1 ] && command -v brew >/dev/null 2>&1; then
            say 'Installing mpv with Homebrew'
            brew install mpv
        else
            warn '  brew install mpv'
        fi
    else
        if command -v apt-get >/dev/null 2>&1; then
            warn '  sudo apt install mpv'
        elif command -v dnf >/dev/null 2>&1; then
            warn '  sudo dnf install mpv'
        elif command -v pacman >/dev/null 2>&1; then
            warn '  sudo pacman -S mpv'
        fi
    fi
fi

printf '\n'
say 'Running am doctor'
if ! "${DIR}/am" doctor; then
    warn 'am doctor reported something above that still needs attention'
fi

printf '\n'
printf 'am is installed.\n'
printf '  binary  %s/am\n' "$DIR"
printf '\n'
printf 'Next:\n'
printf '  am login                      sign in with your browser, once\n'
printf '  am playlists                  your library\n'
printf '  am search "instant crush"     the catalogue\n'
if [ "$on_path" -eq 0 ] && [ "$MODIFY_PATH" -eq 1 ]; then
    printf '\n'
    printf 'Open a new shell for the PATH change to apply everywhere.\n'
fi
printf '\n'
