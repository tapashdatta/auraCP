// {{cache_skip}} / {{fastcgi_cache}} / {{proxy_cache}} → nginx caching.
//
// Three placeholders because cache directives live in three different
// scopes:
//
//   - {{cache_skip}}    at server-level: sets $skip_cache based on
//                       POST / query-string / WP-login cookies. Used
//                       by both fastcgi_cache_bypass and proxy_cache_bypass.
//   - {{fastcgi_cache}} inside the `location ~ \.php$` block: emits
//                       fastcgi_cache + fastcgi_cache_valid + bypass + no-cache.
//   - {{proxy_cache}}   inside the `location / { proxy_pass ... }` block
//                       (Node/Python templates): same shape but for proxy_cache.
//
// Disabled (Cache=false) → all three expand to nothing. The cache zones
// auracp_fastcgi / auracp_proxy must be declared in /etc/nginx/nginx.conf
// (installer drops them via fastcgi_cache_path / proxy_cache_path).
//
// CacheTTL defaults to "600s" if unset — matches what the legacy
// renderer did.
package processor

import "fmt"

const phCacheSkip = "{{cache_skip}}"
const phFastcgiCache = "{{fastcgi_cache}}"
const phProxyCache = "{{proxy_cache}}"

const cacheSkipBlock = `set $skip_cache 0;
    if ($request_method = POST)            { set $skip_cache 1; }
    if ($query_string != "")               { set $skip_cache 1; }
    if ($http_cookie ~* "comment_author|wordpress_logged_in|wp-postpass") { set $skip_cache 1; }`

func resolveCacheTTL(ttl string) string {
	if ttl == "" {
		return "600s"
	}
	return ttl
}

func CacheSkip(in string, ctx *SiteContext) string {
	if !ctx.Cache {
		return Replace(in, phCacheSkip, "")
	}
	return Replace(in, phCacheSkip, cacheSkipBlock)
}

func FastcgiCache(in string, ctx *SiteContext) string {
	if !ctx.Cache {
		return Replace(in, phFastcgiCache, "")
	}
	val := fmt.Sprintf(`fastcgi_cache auracp_fastcgi;
        fastcgi_cache_valid 200 301 302 %s;
        fastcgi_cache_bypass $skip_cache;
        fastcgi_no_cache    $skip_cache;
        add_header X-Cache $upstream_cache_status always;`, resolveCacheTTL(ctx.CacheTTL))
	return Replace(in, phFastcgiCache, val)
}

func ProxyCache(in string, ctx *SiteContext) string {
	if !ctx.Cache {
		return Replace(in, phProxyCache, "")
	}
	val := fmt.Sprintf(`proxy_cache auracp_proxy;
        proxy_cache_valid 200 301 302 %s;
        proxy_cache_bypass $skip_cache;
        proxy_no_cache     $skip_cache;
        proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;
        add_header X-Cache $upstream_cache_status always;`, resolveCacheTTL(ctx.CacheTTL))
	return Replace(in, phProxyCache, val)
}
