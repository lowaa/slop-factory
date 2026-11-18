package llmusage

import (
	"regexp"
	"strings"
)

// Tool categories. The set is fixed because it is a metric label; the only
// open-ended one is CategoryMCPPrefix+<server>, bounded by the MCP servers
// agents are configured with and by mcpServerPattern.
const (
	CategoryMCPPrefix = "mcp:"
	CategoryBash      = "bash"
	CategoryFileRead  = "file_read"
	CategoryFileEdit  = "file_edit"
	CategorySearch    = "search"
	CategoryWeb       = "web"
	CategorySubagent  = "subagent"
	CategoryOther     = "other"

	// SlopHausMCPServer is the server name slop-haus's MCP tools appear
	// under, in Claude Code (mcp__slop-haus__<tool>) and, via pi's
	// extension, as its own category.
	SlopHausMCPServer = "slop-haus"
)

var fixedCategories = map[string]bool{
	CategoryBash: true, CategoryFileRead: true, CategoryFileEdit: true, CategorySearch: true,
	CategoryWeb: true, CategorySubagent: true, CategoryOther: true,
}

var mcpServerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidCategory reports whether c is one Categorize can return.
func ValidCategory(c string) bool {
	if server, ok := strings.CutPrefix(c, CategoryMCPPrefix); ok {
		return mcpServerPattern.MatchString(server)
	}
	return fixedCategories[c]
}

var claudeTools = map[string]string{
	"Bash":         CategoryBash,
	"BashOutput":   CategoryBash,
	"KillShell":    CategoryBash,
	"Read":         CategoryFileRead,
	"NotebookRead": CategoryFileRead,
	"Edit":         CategoryFileEdit,
	"MultiEdit":    CategoryFileEdit,
	"Write":        CategoryFileEdit,
	"NotebookEdit": CategoryFileEdit,
	"Grep":         CategorySearch,
	"Glob":         CategorySearch,
	"LS":           CategorySearch,
	"WebFetch":     CategoryWeb,
	"WebSearch":    CategoryWeb,
	"Task":         CategorySubagent,
	"Agent":        CategorySubagent,
}

var piTools = map[string]string{
	"bash":       CategoryBash,
	"powershell": CategoryBash,
	"read":       CategoryFileRead,
	"edit":       CategoryFileEdit,
	"write":      CategoryFileEdit,
	"grep":       CategorySearch,
	"find":       CategorySearch,
	"ls":         CategorySearch,
}

// slopHausTools are the slop-haus MCP server's tools. pi has no MCP client,
// so slop-haus's pi extension registers them as plain pi tools under these
// names; they count as mcp:slop-haus because that is what they stand in
// for. A tool the server adds later lands in "other" until it's listed here.
var slopHausTools = map[string]bool{
	"checkin": true, "ticket_next": true, "ticket_wait": true, "ticket_get": true,
	"ticket_search": true, "ticket_comment": true, "ticket_edit": true,
	"ticket_scope_extend": true, "ticket_release": true, "ticket_discuss": true,
	"ticket_prepare": true, "ticket_review": true, "ticket_finish": true, "ticket_done": true,
	"ticket_create": true, "ticket_blocker_add": true, "ticket_blocker_remove": true,
	"chat_post": true, "chat_read": true, "integration_lock": true, "integration_unlock": true,
}

// slopReviewTools are a code-review harness's reviewer tools. Its git_* tools read
// repository content through fixed git queries, so they are file reads,
// not bash.
var slopReviewTools = map[string]string{
	"read_file": CategoryFileRead,
	"get_diff":  CategoryFileRead,
	"git_show":  CategoryFileRead,
	"git_log":   CategoryFileRead,
	"git_blame": CategoryFileRead,
	"list_dir":  CategorySearch,
	"grep":      CategorySearch,
}

// Categorize maps a raw tool name, as harness reported it, to its category
// and its name within that category: "mcp__slop-haus__ticket_get" is
// ("mcp:slop-haus", "ticket_get"); everything else keeps its name. A tool
// it doesn't know is CategoryOther.
func Categorize(harness Harness, toolName string) (category, tool string) {
	if rest, ok := strings.CutPrefix(toolName, "mcp__"); ok {
		server, name, found := strings.Cut(rest, "__")
		if found && name != "" && mcpServerPattern.MatchString(server) {
			return CategoryMCPPrefix + server, name
		}
		return CategoryOther, toolName
	}
	var table map[string]string
	switch harness {
	case HarnessClaude:
		table = claudeTools
	case HarnessPi:
		if slopHausTools[toolName] {
			return CategoryMCPPrefix + SlopHausMCPServer, toolName
		}
		table = piTools
	case HarnessSlopReview:
		table = slopReviewTools
	}
	if c, ok := table[toolName]; ok {
		return c, toolName
	}
	return CategoryOther, toolName
}
