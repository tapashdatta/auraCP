// {{nginx_access_log}} / {{nginx_error_log}} → per-site log paths
//
// Logs land under /home/<user>/logs/ — same dir as the runtime logs so
// the operator has one tail target per site, regardless of which
// component (nginx, php-fpm, node, gunicorn) generated which line. The
// logs dir is created by the Creator before nginx reload; nginx workers
// drop privileges from www-data to <site-user>'s group (via per-site
// FPM pool socket ownership) and so need the dir traversable from
// www-data — we set /home/<user>/ to mode 750 with group www-data so
// nginx-as-www-data can `cd` in.
package processor

import "fmt"

const phNginxAccessLog = "{{nginx_access_log}}"
const phNginxErrorLog = "{{nginx_error_log}}"

func NginxAccessLog(in string, ctx *SiteContext) string {
	if ctx.LogDir == "" {
		return Replace(in, phNginxAccessLog, "")
	}
	value := fmt.Sprintf("access_log %s/access.log;", ctx.LogDir)
	return Replace(in, phNginxAccessLog, value)
}

func NginxErrorLog(in string, ctx *SiteContext) string {
	if ctx.LogDir == "" {
		return Replace(in, phNginxErrorLog, "")
	}
	value := fmt.Sprintf("error_log %s/error.log;", ctx.LogDir)
	return Replace(in, phNginxErrorLog, value)
}
