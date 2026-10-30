#!/bin/sh
# Runs outside freedom-bot so a broken replacement cannot disable its own recovery.
set -eu
if [ "$#" -ne 3 ]; then exit 2; fi
target=$1
backup=$2
expected_sha=$3
ready=/run/freedom-bot.ready
cleanup() { rm -f -- "$0"; }
trap cleanup EXIT HUP INT TERM
case "$expected_sha" in *[!0-9a-f]*|"") echo "invalid sha" >&2; exit 2;; esac
if [ "${#expected_sha}" -ne 64 ]; then exit 2; fi
if [ ! -s "$backup" ] || [ ! -x "$target" ]; then echo "backup/target missing" >&2; exit 2; fi
rollback() {
  echo "freedom-bot update health check failed; restoring previous binary" >&2
  recovery="${target}.recovery.$$"
  if cp -p -- "$backup" "$recovery" && chmod 0755 "$recovery" && mv -f -- "$recovery" "$target"; then
    systemctl restart freedom-bot.service || true
    systemctl is-active --quiet freedom-bot.service || echo "rollback restart failed; SSH recovery required" >&2
  else
    rm -f -- "$recovery"
    echo "failed to restore backup; SSH recovery required" >&2
  fi
}
rm -f -- "$ready"
if ! systemctl restart freedom-bot.service; then rollback; exit 1; fi
# First interval allows Telegram getMe to authenticate and write readiness marker.
# Subsequent checks detect crash/restart loops after the first successful start.
for delay in 25 10; do
  sleep "$delay"
  if ! systemctl is-active --quiet freedom-bot.service ||
     [ ! -r "$ready" ] || [ "$(cat "$ready")" != "$expected_sha" ]; then
    rollback
    exit 1
  fi
done
echo "freedom-bot update health checks passed"
