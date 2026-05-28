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

const panelTemplate = `# auraCP control panel — fronts auracpd's :8443 self-signed TLS.
server {
    listen 80;
    listen [::]:80;
    server_name {{.Domain}};

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

    location / {
        proxy_pass {{.Backend}};
        proxy_ssl_verify off;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 600s;
        proxy_buffering off;
    }
}
{{- end }}
`
