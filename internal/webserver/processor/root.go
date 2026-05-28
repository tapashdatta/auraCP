// {{root}} → "root /home/<user>/htdocs/<domain>;"
//
// Reads ctx.DocRoot, which the Creator pipeline already resolved to the
// absolute path (template-supplied `rootDirectory` suffix already
// appended). The processor never recomputes the path from User+Domain —
// that's the field-of-truth pattern: every consumer reads ctx.DocRoot,
// every artifact writer sets ctx.DocRoot, they cannot drift.
package processor

import "fmt"

const phRoot = "{{root}}"

func Root(in string, ctx *SiteContext) string {
	value := fmt.Sprintf("root %s;", ctx.DocRoot)
	return Replace(in, phRoot, value)
}
