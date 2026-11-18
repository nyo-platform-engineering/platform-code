#!/usr/bin/env bash
# Run independently of k3s provisioning, including on existing guests.
set -euo pipefail
role="${1:?server or agent}"; server_ip="${2:?control-plane IP}"
[[ "$role" == server || "$role" == agent ]] || exit 2
[[ "$server_ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || exit 2

if ! command -v chronyc >/dev/null; then
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq chrony
fi

temporary="$(mktemp)"
trap 'rm -f "$temporary"' EXIT
{
  echo '# Managed by AAT local/sync-clock.sh.'
  echo 'driftfile /var/lib/chrony/chrony.drift'
  echo 'rtcsync'
  # Prefer timestamp alignment over slow frequency smoothing in this VM lab.
  # The default ratio (3) can leave milliseconds of pending correction for minutes.
  echo 'corrtimeratio 0.1'
  # Step only large startup errors; ordinary corrections slew the running clock.
  echo 'makestep 1.0 3'
  if [[ "$role" == server ]]; then
    echo 'pool ntp.ubuntu.com iburst maxpoll 6'
    # Only the two lab workers may use this VM as an NTP server.
    printf 'bindaddress %s\n' "$server_ip"
    printf 'allow %s.11\nallow %s.12\n' "${server_ip%.*}" "${server_ip%.*}"
  else
    # Frequent LAN samples keep relative timestamps useful for short HTTP spans.
    printf 'server %s iburst prefer minpoll 0 maxpoll 0\n' "$server_ip"
    echo 'pool ntp.ubuntu.com iburst minpoll 6 maxpoll 8'
  fi
} > "$temporary"

changed=false
if ! cmp -s "$temporary" /etc/chrony/chrony.conf; then
  install -m 644 "$temporary" /etc/chrony/chrony.conf
  changed=true
fi
# chrony's package replaces timesyncd on Ubuntu; also cover guests retaining it.
if systemctl list-unit-files systemd-timesyncd.service --no-legend | grep -q '^systemd-timesyncd.service'; then
  systemctl disable --now systemd-timesyncd.service
fi
systemctl enable --now chrony.service
if [[ "$changed" == true ]]; then
  systemctl restart chrony.service
fi

# Do not silently declare an unreachable NTP source healthy. Bound each synchronization check to 60s.
if ! chronyc waitsync 60 0.001 0 1; then
  chronyc tracking
  chronyc -n sources
  echo 'Clock did not synchronize within 1 ms; check NTP reachability.' >&2
  exit 1
fi
# A fresh control daemon needs samples before it becomes a usable NTP server.
# Do not accept an upstream fallback as proof of the intended shared reference.
if [[ "$role" == agent ]]; then
  selected=false
  for ((attempt = 0; attempt < 60; attempt++)); do
    if chronyc -n sources | awk -v ip="$server_ip" '$1 == "^*" && $2 == ip {found=1} END {exit !found}'; then
      selected=true
      break
    fi
    sleep 1
  done
  if [[ "$selected" != true ]]; then
    chronyc -n sources
    echo 'Worker did not select the control VM as its NTP reference.' >&2
    exit 1
  fi
  chronyc waitsync 60 0.001 0 1
fi
chronyc tracking
chronyc -n sources
