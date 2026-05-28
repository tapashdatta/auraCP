// {{basic_auth}} → auth_basic + auth_basic_user_file directives.
//
// The htpasswd file at ctx.BasicAuthFile is written by
// creator.RunReapply (the same caller that populates this context
// field), so by the time nginx loads the vhost the file exists. If
// BasicAuthUser is empty the directives are elided cleanly.
package processor

import "fmt"

const phBasicAuth = "{{basic_auth}}"

func BasicAuth(in string, ctx *SiteContext) string {
	if ctx.BasicAuthUser == "" {
		return Replace(in, phBasicAuth, "")
	}
	val := fmt.Sprintf(`auth_basic "Restricted";
    auth_basic_user_file %s;`, ctx.BasicAuthFile)
	return Replace(in, phBasicAuth, val)
}
