package webserver

// nginx vhost template — one file renders all five site types. Switching is
// done with template `{{if}}` blocks so each generated server{} stays
// minimal (no commented-out chunks). Cert paths point at auracpd-managed
// LE outputs in /etc/auracp/ssl; until lego has issued a cert the cert_path
// is empty and the template emits an HTTP-only vhost (the ACME location
// block stays so the next challenge can complete and trigger a reload).

const vhostTemplate = `# auraCP-managed vhost — do not edit by hand; rewritten on every panel change.
{{- if .Bots }}
map $http_user_agent $auracp_bad_bot_{{.SafeName}} {
    default 0;
    "~*(ahrefsbot|semrushbot|mj12bot|dotbot|petalbot)" 1;
}
{{- end }}

server {
    listen 80;
    listen [::]:80;
    server_name {{.Domain}};

    # ACME HTTP-01 challenge — always served plaintext on :80, even after the
    # cert lands. Renewals need it.
    location /.well-known/acme-challenge/ {
        alias {{.ACMEDir}}/;
        default_type "text/plain";
        try_files $uri =404;
    }

    {{- if .CertPath }}
    # Cert is in place — everything else upgrades to HTTPS.
    location / { return 301 https://$host$request_uri; }
    {{- else }}
    {{ template "body" . }}
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
    ssl_prefer_server_ciphers off;

    add_header Strict-Transport-Security "max-age=31536000" always;
    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;

    {{ template "body" . }}
}
{{- end }}

{{- define "body" }}
    access_log {{.LogDir}}/access.log;
    error_log  {{.LogDir}}/error.log;

    server_tokens off;
    client_max_body_size 64m;

    {{- if .Bots }}
    if ($auracp_bad_bot_{{.SafeName}}) { return 403; }
    {{- end }}

    {{- if .BasicAuthUser }}
    auth_basic "Restricted";
    auth_basic_user_file {{.BasicAuthFile}};
    {{- end }}

    # Deny dotfiles (except the ACME challenge alias above).
    location ~ /\.(?!well-known) { deny all; }

    {{- if eq .Type "static" }}
    root {{.DocRoot}};
    index index.html index.htm;
    location / {
        try_files $uri $uri/ =404;
    }

    {{- else if or (eq .Type "php") (eq .Type "wordpress") }}
    root {{.DocRoot}};
    index index.php index.html;

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    {{- if .Cache }}
    set $skip_cache 0;
    if ($request_method = POST)            { set $skip_cache 1; }
    if ($query_string != "")               { set $skip_cache 1; }
    if ($http_cookie ~* "comment_author|wordpress_logged_in|wp-postpass") { set $skip_cache 1; }
    {{- end }}

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_pass unix:{{.PHPSocket}};
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param HTTPS $https if_not_empty;
        fastcgi_read_timeout 120s;
        {{- if .Cache }}
        fastcgi_cache auracp_fastcgi;
        fastcgi_cache_valid 200 301 302 {{.CacheTTL}};
        fastcgi_cache_bypass $skip_cache;
        fastcgi_no_cache    $skip_cache;
        add_header X-Cache $upstream_cache_status always;
        {{- end }}
    }

    {{- else if or (eq .Type "nodejs") (eq .Type "python") }}
    location / {
        proxy_pass http://{{.Upstream}};
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $http_connection;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
        {{- if .Cache }}
        proxy_cache auracp_proxy;
        proxy_cache_valid 200 301 302 {{.CacheTTL}};
        proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;
        add_header X-Cache $upstream_cache_status always;
        {{- end }}
    }

    {{- else if eq .Type "reverseproxy" }}
    location / {
        proxy_pass {{.Upstream}};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
    }
    {{- end }}
{{- end }}
`

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
        # Static files (Adminer CSS/JS): serve directly.
        location ~ ^/_adminer/.+\.(css|js|png|svg|ico|woff2?)$ {
            alias /opt/auracp/adminer/;
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
