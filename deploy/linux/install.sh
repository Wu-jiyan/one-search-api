#!/usr/bin/env bash
# Install One Search API on this host. Safe to re-run.
set -euo pipefail

APP_DIR=/www/wwwroot/onesearch
VHOST=/www/server/panel/vhost/nginx/onesearch-api.conf
UNIT=/etc/systemd/system/onesearch.service
BIN=${APP_DIR}/onesearch

log() { echo "[onesearch-install] $*"; }

# --- service account -------------------------------------------------------
if ! id onesearch >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin onesearch
  log "created system user onesearch"
fi

# --- keys ------------------------------------------------------------------
if grep -q '__KEY_NEWAPI__' ${APP_DIR}/config.yaml; then
  KEY_NEWAPI="sk-onesearch-$(openssl rand -hex 24)"
  KEY_ME="sk-onesearch-$(openssl rand -hex 24)"
  sed -i "s|__KEY_NEWAPI__|${KEY_NEWAPI}|; s|__KEY_ME__|${KEY_ME}|" ${APP_DIR}/config.yaml
  echo "${KEY_NEWAPI}" > ${APP_DIR}/key.newapi
  echo "${KEY_ME}" > ${APP_DIR}/key.me
  chmod 600 ${APP_DIR}/key.newapi ${APP_DIR}/key.me
  log "generated API keys"
fi

chown root:onesearch ${APP_DIR}/config.yaml
chmod 640 ${APP_DIR}/config.yaml
chown root:root ${BIN}
chmod 755 ${BIN}

# --- systemd ---------------------------------------------------------------
cp ${APP_DIR}/onesearch.service ${UNIT}
systemctl daemon-reload
systemctl enable onesearch >/dev/null
systemctl restart onesearch
log "onesearch service started"

# --- nginx -----------------------------------------------------------------
cp ${APP_DIR}/onesearch-api.conf ${VHOST}
nginx -t
systemctl reload nginx
log "nginx vhost installed"

# --- firewall: keep the new ports private ----------------------------------
for rule in \
  "103.116.245.218 18091" \
  "103.116.245.218 18092" \
  "172.16.0.0/12 18091" \
  "172.16.0.0/12 18092" \
  "10.0.0.0/8 18092"; do
  set -- ${rule}
  if ! ufw status | grep -qE "ALLOW +${1} .*${2}"; then
    ufw allow from ${1} to any port ${2} proto tcp >/dev/null
    log "ufw allow ${1} -> ${2}"
  fi
done

# --- searxng ---------------------------------------------------------------
if [ -f ${APP_DIR}/searxng/docker-compose.yml ]; then
  if grep -q '__SearXNG_SECRET__' ${APP_DIR}/searxng/settings.yml; then
    sed -i "s|__SearXNG_SECRET__|$(openssl rand -hex 32)|" ${APP_DIR}/searxng/settings.yml
    log "generated searxng secret key"
  fi
  cd ${APP_DIR}/searxng
  docker compose up -d
  log "searxng container started"
fi

sleep 3
systemctl --no-pager --lines=5 status onesearch || true
curl -sS --max-time 5 http://127.0.0.1:18090/healthz || true
echo
log "done"
