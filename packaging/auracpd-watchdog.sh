#!/bin/sh
# auracpd-watchdog — runs on a 60-second timer. Polls /api/health up to 3 times
# (5 s apart). If every probe fails, restarts auracpd. Belt-and-braces against
# the documented systemd corner case where Restart=always does NOT re-fire
# after a clean `systemctl stop` (which is exactly what happens during a
# panel-initiated `dpkg -i` upgrade — without this watchdog the box can sit at
# 502 indefinitely after a botched update, unreachable from the UI).
#
# Stays silent on the happy path so journalctl isn't spammed; only logs
# (via systemd-cat → journald) when it actually has something to do.
set -u

URL="https://127.0.0.1:8443/api/health"
fails=0
for i in 1 2 3; do
    if curl -kfsS "$URL" -o /dev/null --max-time 3 2>/dev/null; then
        exit 0          # healthy on any probe → done
    fi
    fails=$((fails + 1))
    [ "$i" -lt 3 ] && sleep 5
done

# Three strikes → restart. systemctl exit ≠ 0 still gets logged.
logger -t auracpd-watchdog "auracpd /api/health failed ${fails}x — issuing restart"
systemctl restart auracpd
