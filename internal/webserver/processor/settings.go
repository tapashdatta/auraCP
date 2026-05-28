// {{settings}} → operator's freeform nginx config (Vhost-tab textarea).
//
// Always runs LAST in the processor chain so other placeholders embedded
// in the operator's text get a chance to substitute too. Empty string
// when the operator hasn't customized — the resulting blank line gets
// cleaned up by Template.RemoveEmptyPlaceholders().
//
// Safety: the full rendered vhost goes through `nginx -t` before being
// made live (Creator.CreateNginxVhost does an atomic stage→test→swap),
// so a syntactically broken settings block can't take a site down.
package processor

const phSettings = "{{settings}}"

func Settings(in string, ctx *SiteContext) string {
	return Replace(in, phSettings, ctx.Settings)
}
