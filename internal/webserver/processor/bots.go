// {{bot_map}} / {{bot_check}} → User-Agent deny-list emission.
//
// Two placeholders because the directives go in two different lexical
// scopes in the nginx vhost:
//   - {{bot_map}}   at the http-level (outside server{})
//                   emits the `map $http_user_agent $auracp_bad_bot_<safe>` block
//   - {{bot_check}} inside the server{} block (after server_name)
//                   emits `if ($auracp_bad_bot_<safe>) { return 403; }`
//
// Disabled (BlockBots=false) → both expand to nothing. The template's
// blank-line collapser cleans up the empty lines after removeEmptyPlaceholders.
package processor

import (
	"fmt"
	"strings"
)

const phBotMap = "{{bot_map}}"
const phBotCheck = "{{bot_check}}"

// botMapDeniedAgents pins the UA-fragment list we always reject. CloudPanel
// ships a similar (longer) list; we stay conservative and only block the
// ones that are demonstrably automated SEO/SCM bots with no legitimate
// site-visitor traffic.
var botMapDeniedAgents = []string{"ahrefsbot", "semrushbot", "mj12bot", "dotbot", "petalbot"}

// safeName converts a domain into an nginx-identifier-safe suffix. The
// resulting `$auracp_bad_bot_<safe>` variable name needs to be unique
// across the host's vhosts (otherwise nginx warns about duplicate
// map keys at reload).
func safeName(domain string) string {
	r := strings.NewReplacer(".", "_", "-", "_")
	return r.Replace(domain)
}

func BotMap(in string, ctx *SiteContext) string {
	if !ctx.BlockBots {
		return Replace(in, phBotMap, "")
	}
	val := fmt.Sprintf(`map $http_user_agent $auracp_bad_bot_%s {
    default 0;
    "~*(%s)" 1;
}`, safeName(ctx.Domain), strings.Join(botMapDeniedAgents, "|"))
	return Replace(in, phBotMap, val)
}

func BotCheck(in string, ctx *SiteContext) string {
	if !ctx.BlockBots {
		return Replace(in, phBotCheck, "")
	}
	val := fmt.Sprintf("if ($auracp_bad_bot_%s) { return 403; }", safeName(ctx.Domain))
	return Replace(in, phBotCheck, val)
}
