#!/usr/bin/env bash
#
# Bring up a throwaway Radarr in Docker, lay out a known catalogue of tiny
# films for it, and print the environment the live test suites need.
#
#   eval "$(scripts/testenv.sh up)"   # start, export RADARR_*
#   scripts/testenv.sh down           # stop and remove everything
#   scripts/testenv.sh fixtures       # write the media tree only
#
# This script only does what radarr-mcp cannot: lay the films out on disk,
# write the config Radarr reads its API key from, and run the container. The
# root folders, the films, the indexer, the download client and every edit
# are the test suites' job, through rootfolder_add, movie_add and the rest,
# so those tools are exercised rather than bypassed.
#
# The container is pointed at the record/replay proxy the tests run (see
# lib/providerproxy): Radarr, not radarr-mcp, is what calls its metadata
# service (api.radarr.video) and the image CDN, so intercepting those calls
# has to happen at its edge. Radarr is a .NET app, which honours HTTPS_PROXY
# and, on Linux, trusts whatever SSL_CERT_FILE names - so the proxy's
# certificate authority is minted here (openssl) and mounted in, and the
# tests load the same files to sign with.

set -euo pipefail

# pinned, because what Radarr asks its metadata service is what the cassettes
# replay: a new release can ask differently. To move to one, set
# RADARR_TEST_IMAGE to it, run make record, and bump this.
IMAGE="${RADARR_TEST_IMAGE:-lscr.io/linuxserver/radarr:version-6.4.4.10685}"
PORT="${RADARR_TEST_PORT:-17878}"
NAME="${RADARR_TEST_CONTAINER:-radarr-mcp-test}"
# the port the tests' provider proxy listens on, reached from inside the
# container via host.docker.internal
PROXY_PORT="${RADARR_TEST_PROXY_PORT:-17880}"
# the port the tests' fake Newznab indexer listens on, reached the same way
# but directly (NO_PROXY), so a search never needs a cassette
INDEXER_PORT="${RADARR_TEST_INDEXER_PORT:-17881}"
# not TMPDIR: on macOS that is /var/folders/..., which Docker Desktop and
# Colima do not share by default, and the bind mounts silently come up empty
DATA="${RADARR_TEST_DATA:-${HOME}/.cache/radarr-mcp/testenv/default}"
# the proxy's certificate authority, shared by every container so one set of
# cassettes serves them all
PROXY_CA="${RADARR_TEST_PROXY_CA:-${HOME}/.cache/radarr-mcp/testenv/proxy}"
# Radarr reads its API key from config.xml, which is written before the first
# start, so the key is known without scraping it out of a running server. It
# is a throwaway: the container is on localhost and gone after the run.
API_KEY="radarrmcp0test0key0000000000000a"
URL="http://127.0.0.1:${PORT}"

# proxy_host is the address the container reaches the test proxy and the fake
# indexer on. Docker Desktop and Colima provide host.docker.internal; on Linux
# docker maps it with --add-host, and we hand the server the bridge gateway's
# address instead, so a runtime that prefers an IPv6 answer cannot pick a
# route the host does not listen on.
proxy_host() {
  if [ "$(uname -s)" = "Linux" ]; then
    docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null && return 0
  fi
  echo "host.docker.internal"
}

log() { echo "==> $*" >&2; }

# ---------------------------------------------------------------------------
# The catalogue. Real films with their real TMDB ids and runtimes, so what
# Radarr's metadata service says about them (recorded through the proxy) and
# what the audits compare against is true. The files are fakes: a still frame
# at a tenth of a frame a second runs the film's length in tens of kilobytes,
# so Radarr probes a real container with a real resolution, codec, runtime
# and audio language. Change a line here and the suites' fixture tables
# (acceptance/harness_test.go, integration/harness_test.go) must agree.
#
# The clean films, one folder each under /media/movies, every file named the
# way Radarr's default naming scheme would name it, 1080p h264 at the film's
# runtime, with audio in the film's original language.
#
# folder|title|year|tmdb|imdb|runtime minutes|audio language
MOVIES='Alien (1979)|Alien|1979|348|tt0078748|117|eng
Aliens (1986)|Aliens|1986|679|tt0090605|137|eng
Blade Runner (1982)|Blade Runner|1982|78|tt0083658|118|eng
Dune (2021)|Dune|2021|438631|tt1160419|155|eng
Dune - Part Two (2024)|Dune: Part Two|2024|693134|tt15239678|167|eng
Princess Mononoke (1997)|Princess Mononoke|1997|128|tt0119698|134|jpn
Arrival (2016)|Arrival|2016|329865|tt2543164|116|eng
The Thirteenth Floor (1999)|The Thirteenth Floor|1999|1090|tt0139809|100|eng'

# The messy films, under /media/messy: the defects the audits exist to find,
# laid out the way a real collection accumulates them. The suites add each
# with movie_add; some carry more than one defect, the way a wrong match
# usually does.
#
#   Dune (2021)              added as Dune (1984), tmdb 841: the folder says
#                            2021 and the match says 1984 (audit_year_mismatch),
#                            and the file is not named as the 1984 film would
#                            be (audit_naming)
#   Interstellar (2014)      a 169 minute film whose file runs 40: a truncated
#                            download (audit_runtime)
#   Blade Runner 2049 (2017) named Bluray-2160p, but the video is 1280x720: a
#                            720p file passed off as 4K (audit_resolution_mismatch)
#   Spirited Away (2001)     a DVD rip: an .avi of 720x480 XviD
#                            (audit_quality), whose audio carries no language
#                            tag, as AVI cannot (so audit_language leaves it be)
#   Akira (1988)             dubbed: English audio only, of a Japanese film
#                            (audit_language)
#   Heat (1995)              an HDTV-720p file on the HD-1080p profile, whose
#                            cutoff is Bluray-1080p (audit_cutoff_unmet)
#   The Matrix (1999)        a scene-named file Radarr would rename
#                            (audit_naming)
#   Contact (1997)           two copies in one folder, 1080p and 2160p: Radarr
#                            tracks one, the other sits there
#                            (audit_untracked_files)
#   Gattaca (1997)           a WEBDL-1080p file the size of a thumbnail, which
#                            the suites catch by giving WEBDL-1080p a minimum
#                            size (audit_size)
#
# Beside them, folders no film is added for:
#
#   /media/movies/Ronin (1998)              a film on disk Radarr has never
#                                           heard of (audit_unmapped_folders)
#   /media/messy/Alien (1979) Directors Cut a second copy of a film Radarr
#                                           tracks elsewhere (the same audit,
#                                           which names the copy it duplicates)
#
# and films added with no folder at all (in the suites, not here): Alien³,
# monitored and missing (audit_missing_files), and Alien Resurrection,
# unmonitored (audit_unmonitored).
#
# folder|file|runtime minutes|size|codec|audio language
MESSY='Dune (2021)|Dune (2021) Bluray-1080p.mkv|136|1920x1080|h264|eng
Interstellar (2014)|Interstellar (2014) Bluray-1080p.mkv|40|1920x1080|h264|eng
Blade Runner 2049 (2017)|Blade Runner 2049 (2017) Bluray-2160p.mkv|164|1280x720|h264|eng
Spirited Away (2001)|Spirited Away (2001) DVDRip XviD.avi|125|720x480|xvid|eng
Akira (1988)|Akira (1988) Bluray-1080p.mkv|124|1920x1080|h264|eng
Heat (1995)|Heat (1995) HDTV-720p.mkv|170|1280x720|h264|eng
The Matrix (1999)|the.matrix.1999.1080p.bluray.x264-GRP.mkv|136|1920x1080|h264|eng
Contact (1997)|Contact (1997) Bluray-1080p.mkv|150|1920x1080|h264|eng
Contact (1997)|Contact (1997) Bluray-2160p.mkv|150|3840x2160|h264|eng
Gattaca (1997)|Gattaca (1997) WEBDL-1080p.mkv|107|1920x1080|h264|eng
Alien (1979) Directors Cut|Alien (1979) Directors Cut Bluray-1080p.mkv|117|1920x1080|h264|eng'

# wipe_data removes the data directory. The container writes its config and
# database as its own user, and on Linux those land in the bind mount owned by
# a user the caller may not be (Docker Desktop and Colima on macOS remap them,
# which is why this only bites in CI). A throwaway root container can always
# remove them.
wipe_data() {
  [ -d "${DATA}" ] || return 0
  rm -rf "${DATA}" 2>/dev/null && return 0

  log "removing container-owned files"
  docker run --rm -v "${DATA}:/data" alpine:3 sh -c 'rm -rf /data/..?* /data/.[!.]* /data/*' >/dev/null 2>&1 || true
  rm -rf "${DATA}" 2>/dev/null || true
}

# video PATH MINUTES [SIZE] [CODEC] [LANGUAGE] - a film of that runtime: one
# still frame every ten seconds, so a two hour 1080p film is tens of
# kilobytes and encodes in well under a second. CODEC is h264, or xvid for
# MPEG-4 part 2 tagged XVID, the legacy codec audit_quality looks for. The
# audio is one second of silence tagged with LANGUAGE, which is all Radarr's
# probe reads - except in an .avi, which has nowhere to put a language and
# takes mp3.
video() {
  local file=$1 minutes=$2 size=${3:-1920x1080} codec=${4:-h264} lang=${5:-eng}
  mkdir -p "$(dirname "$file")"
  local vcodec=libx264 vopts="-preset ultrafast -tune stillimage -crf 51" acodec=aac
  if [ "$codec" = "xvid" ]; then
    vcodec=mpeg4
    vopts="-vtag XVID -q:v 31"
  fi
  case "$file" in *.avi) acodec=libmp3lame ;; esac
  # -nostdin matters: without it ffmpeg reads the while-read loop's stdin
  # looking for interactive keys and swallows a character of the next line
  # shellcheck disable=SC2086 # vopts is a list of flags
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "color=c=0x202030:s=${size}:r=1/10" -f lavfi -t 1 -i "anullsrc=r=8000:cl=mono" \
    -t "$((minutes * 60))" -c:v "$vcodec" $vopts -g 1000 -pix_fmt yuv420p \
    -c:a "$acodec" -b:a 8k -metadata:s:a:0 "language=${lang}" "$file"
}

fixtures() {
  log "generating the film fixtures under ${DATA}"
  wipe_data

  # the clean films: "<folder>/<folder> Bluray-1080p.mkv"
  while IFS='|' read -r folder _title _year _tmdb _imdb runtime lang; do
    [ -n "$folder" ] || continue
    video "${DATA}/media/movies/${folder}/${folder} Bluray-1080p.mkv" "$runtime" 1920x1080 h264 "$lang"
  done <<<"$MOVIES"

  # the messy ones
  while IFS='|' read -r folder file runtime size codec lang; do
    [ -n "$folder" ] || continue
    video "${DATA}/media/messy/${folder}/${file}" "$runtime" "$size" "$codec" "$lang"
  done <<<"$MESSY"

  # a film on disk that no one adds
  video "${DATA}/media/movies/Ronin (1998)/Ronin (1998) Bluray-1080p.mkv" 122

  # the Usenet Blackhole download client's folders: Radarr writes the .nzb
  # of a grab to one, and imports what appears in the other
  mkdir -p "${DATA}/media/downloads/nzb" "${DATA}/media/downloads/complete" "${DATA}/config"

  # Radarr runs as the calling user (PUID/PGID below), but on a runner whose
  # user differs from the files' owner, or under a remapping VM, it still has
  # to read the media and write its config, and this tree is a throwaway
  chmod -R 777 "${DATA}"
}

# config_xml writes the config.xml Radarr reads on its first start: the API
# key the suites use, no browser, no analytics (Sentry is not provider traffic
# and has no business in a cassette), and debug logging so a failing run
# leaves something to read.
config_xml() {
  cat >"${DATA}/config/config.xml" <<EOF
<Config>
  <BindAddress>*</BindAddress>
  <Port>7878</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>False</LaunchBrowser>
  <ApiKey>${API_KEY}</ApiKey>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <Branch>master</Branch>
  <LogLevel>debug</LogLevel>
  <UrlBase></UrlBase>
  <InstanceName>radarr-mcp-test</InstanceName>
  <AnalyticsEnabled>False</AnalyticsEnabled>
</Config>
EOF
  chmod 666 "${DATA}/config/config.xml"
}

# proxy_ca mints the certificate authority the tests' provider proxy signs
# with, once, so every container started here trusts the same one. The tests
# load these files (providerproxy.Options.CACert/CAKey) rather than minting
# their own.
proxy_ca() {
  [ -f "${PROXY_CA}/ca.pem" ] && [ -f "${PROXY_CA}/ca.key" ] && return 0
  command -v openssl >/dev/null || { echo "openssl is required to mint the provider proxy CA" >&2; exit 1; }
  log "minting the provider proxy CA under ${PROXY_CA}"
  mkdir -p "${PROXY_CA}"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
    -subj "/CN=radarr-mcp provider proxy CA" \
    -keyout "${PROXY_CA}/ca.key" -out "${PROXY_CA}/ca.pem" 2>/dev/null
  chmod 644 "${PROXY_CA}/ca.pem"
}

# wait_for WHAT TRIES COMMAND
wait_for() {
  local what=$1 tries=$2 cmd=$3
  log "waiting for ${what}"
  for _ in $(seq "$tries"); do
    if eval "$cmd" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  echo "timed out waiting for ${what}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  return 1
}

# logs prints what Radarr wrote about itself: docker logs shows its console,
# and /config/logs/radarr.txt the rest.
logs() {
  echo "==> docker logs ${NAME}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  local f="${DATA}/config/logs/radarr.txt"
  [ -f "$f" ] || return 0
  # the errors first: the tail of the log is the last scheduled task, and
  # what went wrong is usually well above it
  echo "==> ${f} (errors)" >&2
  grep -iE 'error|warn|exception|refused|timed out|certificate' "$f" | tail -60 >&2
  echo "==> ${f} (tail)" >&2
  tail -40 "$f" >&2
}

up() {
  command -v ffmpeg >/dev/null || { echo "ffmpeg is required to generate fixtures" >&2; exit 1; }
  command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
  command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }

  down >/dev/null 2>&1 || true
  fixtures
  config_xml
  proxy_ca

  local proxy_at
  proxy_at="$(proxy_host)"
  log "starting ${IMAGE} as ${NAME} on ${PORT} (metadata proxied via ${proxy_at}:${PROXY_PORT})"
  # NO_PROXY carries the container's own name, and the host the fake indexer
  # is on: a search goes straight to it, where the tests answer it
  docker run -d --name "$NAME" \
    -p "${PORT}:7878" \
    --hostname "$NAME" \
    --add-host "host.docker.internal:host-gateway" \
    -e "PUID=$(id -u)" -e "PGID=$(id -g)" -e "TZ=Etc/UTC" \
    -e "HTTP_PROXY=http://${proxy_at}:${PROXY_PORT}" \
    -e "HTTPS_PROXY=http://${proxy_at}:${PROXY_PORT}" \
    -e "http_proxy=http://${proxy_at}:${PROXY_PORT}" \
    -e "https_proxy=http://${proxy_at}:${PROXY_PORT}" \
    -e "NO_PROXY=localhost,127.0.0.1,${NAME},${proxy_at}" \
    -e "no_proxy=localhost,127.0.0.1,${NAME},${proxy_at}" \
    -e "SSL_CERT_FILE=/proxy/ca.pem" \
    -v "${DATA}/media:/media" -v "${DATA}/config:/config" -v "${PROXY_CA}/ca.pem:/proxy/ca.pem:ro" \
    "$IMAGE" >/dev/null

  wait_for "Radarr to answer /api/v3/system/status" 90 "curl -fsS -H 'X-Api-Key: ${API_KEY}' ${URL}/api/v3/system/status"

  # consumed with eval "$(scripts/testenv.sh up)"
  echo "export RADARR_SERVER='${URL}'"
  echo "export RADARR_TOKEN='${API_KEY}'"
  # the tests add the root folders and films themselves over these
  # container-side paths, and add files under RADARR_TEST_DATA to prove a
  # rescan or an import picks them up
  echo "export RADARR_TEST_DATA='${DATA}'"
  echo "export RADARR_TEST_CONTAINER='${NAME}'"
  echo "export RADARR_TEST_PROXY_PORT='${PROXY_PORT}'"
  echo "export RADARR_TEST_PROXY_CA='${PROXY_CA}'"
  echo "export RADARR_TEST_INDEXER_PORT='${INDEXER_PORT}'"
  echo "export RADARR_TEST_HOST='${proxy_at}'"
}

down() {
  log "removing ${NAME}"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  wipe_data
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  fixtures) fixtures ;;  # generate the media tree only, for inspecting the layout
  logs) logs ;;          # what the server wrote about itself, for a failing run
  *) echo "usage: $0 [up|down|fixtures|logs]" >&2; exit 1 ;;
esac
