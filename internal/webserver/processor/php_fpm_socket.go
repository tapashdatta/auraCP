// {{php_fpm_socket}} → "fastcgi_pass unix:/run/php-fpm/<domain>.sock;"
//
// We use a Unix socket (not CloudPanel's TCP loopback). The socket path
// is canonical — no PHP version in it — so swapping a site's PHP version
// only requires moving the pool config file between
// /etc/php/<old>/fpm/pool.d/ and /etc/php/<new>/fpm/pool.d/, plus a
// reload of each version's php-fpm service. The vhost is untouched
// because the socket path doesn't change.
//
// Sweeping stale pool files across all installed PHP versions (so the
// new version's pool can bind the socket) is the Creator's job, not the
// processor's. See internal/site/creator/php.go::CreatePhpFpmPool.
package processor

import "fmt"

const phPhpFpmSocket = "{{php_fpm_socket}}"

func PhpFpmSocket(in string, ctx *SiteContext) string {
	if ctx.FPMSocket == "" {
		// Site type is not PHP/WordPress — placeholder shouldn't appear
		// in the template, but if it slips through, swap it out cleanly.
		return Replace(in, phPhpFpmSocket, "")
	}
	value := fmt.Sprintf("fastcgi_pass unix:%s;", ctx.FPMSocket)
	return Replace(in, phPhpFpmSocket, value)
}
