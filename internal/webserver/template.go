package webserver

// v0.2.38: catch-all default server. Without this, nginx serves the FIRST
// listen-block it loaded when a request's server_name doesn't match any
// configured site — and that first block is typically the panel (since its
// vhost lives at /etc/nginx/sites-enabled/00-panel.conf, alphabetically
// first). The visible symptom: a freshly-created site whose Let's Encrypt
// cert is still pending shows the control panel UI when the operator
// browses to https://<newsite> over HTTPS, because the new site only has
// listen:80 until lego provisions the cert.
//
// This block, written to /etc/nginx/sites-enabled/00-default.conf, takes
// `default_server` on every listen-pair. HTTP returns 444 (close conn);
// HTTPS uses ssl_reject_handshake (nginx 1.19+) so we don't even need a
// snake-oil cert to terminate TLS — nginx rejects the handshake outright.
const catchAllTemplate = `# auraCP — catch-all default server (managed; do not edit by hand)
#
# Drops requests for domains that point at this server but don't have a
# provisioned vhost yet. Without this, nginx serves the first defined
# server block (typically the panel) as a fallback — making freshly
# created sites appear as the panel until their cert lands.

server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 444;
}

server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    ssl_reject_handshake on;
}
`

const panelTemplate = `# auraCP control panel — fronts auracpd's :8443 self-signed TLS.
#
# v0.2.22: upload pipeline directives explicit at the server level so big
# multipart uploads through the file manager don't trip nginx's defaults
# (client_max_body_size 1m, request buffering ON which would force nginx
# to spool the whole upload to disk before passing it to auracpd — killing
# our live progress bar and breaking large files entirely).
server {
    listen 80;
    listen [::]:80;
    server_name {{.Domain}};

    # Allow up to 2 GiB uploads. Adjust at runtime via /etc/nginx/conf.d/.
    client_max_body_size 2g;
    client_body_timeout 600s;
    client_body_buffer_size 256k;

    location /.well-known/acme-challenge/ {
        alias {{.ACMEDir}}/;
        default_type "text/plain";
        try_files $uri =404;
    }
    {{- if .CertPath }}
    location / { return 301 https://$host$request_uri; }
    {{- else }}
    # FIX-3 (INT-2): WebSocket upgrade for /api/dbadmin/sql/stream.
    # The default proxy_pass strips hop-by-hop Upgrade/Connection
    # headers; without these directives the gorilla/websocket upgrader
    # in auracpd rejects the handshake with 400. Declared BEFORE the
    # catch-all "location /" so nginx picks this regex first via the
    # longest-prefix-after-regex rule (regex locations win over the
    # prefix-match "location /").
    location ~ ^/api/dbadmin(/[^/]+)*/sql/stream$ {
        proxy_pass {{.Backend}};
        proxy_ssl_verify off;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
    location / {
        proxy_pass {{.Backend}};
        proxy_ssl_verify off;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_send_timeout 600s;
        proxy_read_timeout 600s;
    }
    {{- end }}
}

{{- if .CertPath }}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name {{.Domain}};

    ssl_certificate     {{.CertPath}};
    ssl_certificate_key {{.KeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;

    add_header Strict-Transport-Security "max-age=31536000" always;

    # Mirror the :80 upload caps — applies to whichever scheme the client used.
    client_max_body_size 2g;
    client_body_timeout 600s;
    client_body_buffer_size 256k;

    # v0.2.25: Adminer (database manager UI). Lives under /_adminer/ on the
    # panel domain so it inherits panel auth (session cookies are scoped to
    # this host) and the panel's TLS. The SSO wrapper at index.php reads a
    # one-time token written by auracpd and pre-authenticates Adminer; no
    # standalone login form is ever shown.
    location /_adminer/ {
        alias /opt/auracp/adminer/;
        index index.php;
        # v0.2.39: serve adminer.css + sibling static assets directly with
        # a long cache; named capture is required because the outer location
        # is regex-anchored and alias needs to know what subpath to serve.
        location ~ ^/_adminer/(?<asset>.+\.(css|js|png|svg|ico|woff2?))$ {
            alias /opt/auracp/adminer/$asset;
            expires 7d;
            access_log off;
        }
        # Route every other request through the PHP wrapper. The wrapper
        # validates the SSO token (or the existing PHP session) and only
        # then hands off to Adminer.
        location ~ ^/_adminer/.*$ {
            include fastcgi_params;
            fastcgi_pass unix:/run/php-fpm/auracp-adminer.sock;
            fastcgi_param SCRIPT_FILENAME /opt/auracp/adminer/index.php;
            fastcgi_param SCRIPT_NAME /_adminer/index.php;
            fastcgi_param PATH_INFO "";
            fastcgi_param HTTPS $https if_not_empty;
            fastcgi_read_timeout 60s;
        }
    }

    # FIX-3 (INT-2): WebSocket upgrade for /api/dbadmin/sql/stream.
    # The default proxy_pass strips hop-by-hop Upgrade/Connection
    # headers; without these directives the gorilla/websocket upgrader
    # in auracpd rejects the handshake with 400. Declared BEFORE the
    # catch-all "location /" so nginx picks this regex first via the
    # longest-prefix-after-regex rule.
    location ~ ^/api/dbadmin(/[^/]+)*/sql/stream$ {
        proxy_pass {{.Backend}};
        proxy_ssl_verify off;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
    location / {
        proxy_pass {{.Backend}};
        proxy_ssl_verify off;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # request_buffering off → nginx pipes the body straight to auracpd
        # as it arrives. Required for our upload progress bar to be honest
        # (without it, the bar finishes when nginx has the bytes, then the
        # browser waits while nginx re-uploads to auracpd — looks like a hang).
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_send_timeout 600s;
        proxy_read_timeout 600s;
    }
}
{{- end }}
`
