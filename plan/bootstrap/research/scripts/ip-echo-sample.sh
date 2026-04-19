#!/usr/bin/env bash
# plan/bootstrap/research/scripts/ip-echo-sample.sh
#
# Hourly sampler for the three default IP-echo services. Logs one TSV row per
# service per poll cycle: timestamp, service, http_status, body_first_64b, parse_outcome.
#
# Intended use: add to crontab with `0 * * * * /path/to/ip-echo-sample.sh`.
# Left runnable but unscheduled in v1; see plan/bootstrap/research/02-ip-echo-availability.md.
set -euo pipefail

OUT="${DDNS_SAMPLE_OUT:-$HOME/ddns-ip-echo-sample.tsv}"
SERVICES=(
  "https://api.ipify.org"
  "https://ifconfig.me/ip"
  "https://icanhazip.com"
)

ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

for svc in "${SERVICES[@]}"; do
  body_file="$(mktemp)"
  code="$(curl -sS -m 10 -o "$body_file" -w '%{http_code}' "$svc" 2>/dev/null || echo "000")"
  body="$(head -c 64 "$body_file" | tr -d '\n\r' | tr '\t' ' ')"
  rm -f "$body_file"

  if [[ "$code" == "000" ]]; then
    outcome="timeout"
  elif [[ "$code" != 2* ]]; then
    outcome="http_$code"
  elif [[ "$body" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    outcome="ok"
  elif [[ "$body" =~ : ]]; then
    outcome="wrong-family"
  else
    outcome="parse-err"
  fi

  printf '%s\t%s\t%s\t%s\t%s\n' "$ts" "$svc" "$code" "$body" "$outcome" >> "$OUT"
done
