#!/usr/bin/env bash
# Corral installer — builds from THIS checkout (no remote download, no
# opaque binary): you can read every line that ends up on your machine.
#
#   git clone https://github.com/corral-sh/corral.git
#   cd corral && ./install.sh
#
# What it does:
#   1. checks macOS ≥ 13.5 (Apple Silicon or Intel)
#   2. installs Lima and Go with Homebrew if they are missing
#   3. builds corral and installs it into /opt/homebrew/bin (or ~/.local/bin)
#   4. records where the source lives so `corral upgrade` can pull + rebuild
set -euo pipefail

BOLD=$'\033[1m'; DIM=$'\033[2m'; AMBER=$'\033[33m'; GREEN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'
say()  { printf '%s\n' "$*"; }
step() { say "${AMBER}→${NC} $*"; }
ok()   { say "${GREEN}✓${NC} $*"; }
die()  { say "${RED}✗${NC} $*" >&2; exit 1; }

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SRC_DIR"
[ -f go.mod ] && grep -q 'corral' go.mod || die "run this from the corral checkout"

say "🐎 ${BOLD}Corral installer${NC} ${DIM}($SRC_DIR)${NC}"

# 1. Platform ---------------------------------------------------------------
[ "$(uname -s)" = "Darwin" ] || die "Corral v0.x supports macOS only (Windows/Linux are on the roadmap)"
macos="$(sw_vers -productVersion)"
major="${macos%%.*}"; rest="${macos#*.}"; minor="${rest%%.*}"
if [ "$major" -lt 13 ] || { [ "$major" -eq 13 ] && [ "${minor:-0}" -lt 5 ]; }; then
  die "macOS $macos is too old: Apple Virtualization with virtiofs needs 13.5+"
fi
ok "macOS $macos ($(uname -m))"

# 2. Dependencies -----------------------------------------------------------
if ! command -v brew >/dev/null 2>&1; then
  die "Homebrew is required (https://brew.sh) — it installs Lima and Go"
fi
if ! command -v limactl >/dev/null 2>&1; then
  step "installing Lima with Homebrew"
  brew install lima
fi
ok "Lima $(limactl --version | awk '{print $3}')"

if ! command -v go >/dev/null 2>&1; then
  step "installing Go (build dependency) with Homebrew"
  brew install go
fi
ok "$(go version | cut -d' ' -f3)"

# 3. Build & install --------------------------------------------------------
VERSION="$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo '')"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
MODULE="github.com/corral-sh/corral"

if [ -w /opt/homebrew/bin ]; then PREFIX=/opt/homebrew; elif [ -w /usr/local/bin ]; then PREFIX=/usr/local; else PREFIX="$HOME/.local"; fi
PREFIX="${CORRAL_PREFIX:-$PREFIX}"
mkdir -p "$PREFIX/bin"

step "building corral $VERSION"
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X $MODULE/internal/cli.Version=$VERSION -X $MODULE/internal/cli.Commit=$COMMIT -X $MODULE/internal/cli.Date=$DATE" \
  -o "$PREFIX/bin/corral" ./cmd/corral
ok "installed $PREFIX/bin/corral"

# 4. Remember the source for `corral upgrade` ----------------------------
mkdir -p "${CORRAL_HOME:-$HOME/.corral}"
printf '{"source":"%s","installed_at":"%s","version":"%s","prefix":"%s"}\n' "$SRC_DIR" "$DATE" "$VERSION" "$PREFIX" \
  > "${CORRAL_HOME:-$HOME/.corral}/install.json"

case ":$PATH:" in
  *":$PREFIX/bin:"*) ;;
  *) say "${AMBER}!${NC} add ${BOLD}$PREFIX/bin${NC} to your PATH, e.g.:  echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.zshrc" ;;
esac

say ""
say "Next:"
say "  ${BOLD}corral setup${NC}     ${DIM}# defaults + prerequisites (1 minute)${NC}"
say "  ${BOLD}cd ~/Code/<project> && corral claude${NC}"
