// {{server_name}} → "server_name a.garuda.sh;" (or with `www.` for apex).
//
// Subdomain detection: we treat anything with >= 3 dot-separated labels
// (a.b.c.tld) as a subdomain and emit a single server_name line. Bare
// apexes (example.com) also get the www. variant so visitors hitting
// http://www.example.com don't need a separate site record.
//
// Multi-label TLD edge case (co.uk, com.au): we don't try to be clever —
// example.co.uk has 3 labels, so we'd skip the www. variant, which is
// actually the right call for those (operators with .co.uk domains
// typically want their site at the bare apex anyway).
package processor

import (
	"fmt"
	"strings"
)

const phServerName = "{{server_name}}"

func ServerName(in string, ctx *SiteContext) string {
	names := []string{ctx.Domain}
	if strings.Count(ctx.Domain, ".") == 1 {
		// apex like example.com — also serve www.example.com
		names = append(names, "www."+ctx.Domain)
	}
	value := fmt.Sprintf("server_name %s;", strings.Join(names, " "))
	return Replace(in, phServerName, value)
}
