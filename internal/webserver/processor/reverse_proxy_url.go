// {{reverse_proxy_url}} → "proxy_pass <user-supplied URL>;"
//
// Preserves the original scheme (http:// vs https://). Validation
// (parseable URL, non-loopback unless the operator explicitly opts in)
// happens upstream in Preflight; by the time this processor runs we
// trust ctx.ReverseProxyURL.
package processor

import "fmt"

const phReverseProxyURL = "{{reverse_proxy_url}}"

func ReverseProxyURL(in string, ctx *SiteContext) string {
	if ctx.ReverseProxyURL == "" {
		return Replace(in, phReverseProxyURL, "")
	}
	value := fmt.Sprintf("proxy_pass %s;", ctx.ReverseProxyURL)
	return Replace(in, phReverseProxyURL, value)
}
