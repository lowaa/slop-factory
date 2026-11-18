package llmusage_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

func TestCategorize(t *testing.T) {
	type tc struct {
		harness  llmusage.Harness
		name     string
		category string
		tool     string
	}
	var cases []tc
	same := func(h llmusage.Harness, category string, names ...string) {
		for _, n := range names {
			cases = append(cases, tc{h, n, category, n})
		}
	}

	same(llmusage.HarnessClaude, llmusage.CategoryBash, "Bash", "BashOutput", "KillShell")
	same(llmusage.HarnessClaude, llmusage.CategoryFileRead, "Read", "NotebookRead")
	same(llmusage.HarnessClaude, llmusage.CategoryFileEdit, "Edit", "MultiEdit", "Write", "NotebookEdit")
	same(llmusage.HarnessClaude, llmusage.CategorySearch, "Grep", "Glob", "LS")
	same(llmusage.HarnessClaude, llmusage.CategoryWeb, "WebFetch", "WebSearch")
	same(llmusage.HarnessClaude, llmusage.CategorySubagent, "Task", "Agent")
	same(llmusage.HarnessClaude, llmusage.CategoryOther, "TodoWrite", "ToolSearch", "ticket_get")

	same(llmusage.HarnessPi, llmusage.CategoryBash, "bash", "powershell")
	same(llmusage.HarnessPi, llmusage.CategoryFileRead, "read")
	same(llmusage.HarnessPi, llmusage.CategoryFileEdit, "edit", "write")
	same(llmusage.HarnessPi, llmusage.CategorySearch, "grep", "find", "ls")
	same(llmusage.HarnessPi, llmusage.CategoryOther, "Bash")
	same(llmusage.HarnessPi, llmusage.CategoryMCPPrefix+llmusage.SlopHausMCPServer,
		"checkin", "ticket_next", "ticket_wait", "ticket_get", "ticket_search", "ticket_comment",
		"ticket_edit", "ticket_scope_extend", "ticket_release", "ticket_discuss", "ticket_prepare",
		"ticket_review", "ticket_finish", "ticket_done", "ticket_create", "ticket_blocker_add", "ticket_blocker_remove",
		"chat_post", "chat_read", "integration_lock", "integration_unlock")

	same(llmusage.HarnessSlopReview, llmusage.CategoryFileRead, "read_file", "get_diff", "git_show", "git_log", "git_blame")
	same(llmusage.HarnessSlopReview, llmusage.CategorySearch, "list_dir", "grep")
	same(llmusage.HarnessSlopReview, llmusage.CategoryOther, "submit_review", "record_finding")
	same(llmusage.HarnessApp, llmusage.CategoryOther, "search_life_events", "Read", "bash")

	for _, h := range []llmusage.Harness{llmusage.HarnessClaude, llmusage.HarnessPi, llmusage.HarnessSlopReview} {
		cases = append(cases,
			tc{h, "mcp__slop-haus__ticket_get", "mcp:slop-haus", "ticket_get"},
			tc{h, "mcp__claude_ai_Claude_Docs__batch", "mcp:claude_ai_Claude_Docs", "batch"},
			tc{h, "mcp__playwright__browser_click", "mcp:playwright", "browser_click"},
			tc{h, "mcp__noseparator", llmusage.CategoryOther, "mcp__noseparator"},
			tc{h, "mcp__server__", llmusage.CategoryOther, "mcp__server__"},
			tc{h, "mcp__bad server__x", llmusage.CategoryOther, "mcp__bad server__x"},
		)
	}

	for _, c := range cases {
		t.Run(string(c.harness)+"/"+c.name, func(t *testing.T) {
			category, tool := llmusage.Categorize(c.harness, c.name)
			assert.Equal(t, c.category, category)
			assert.Equal(t, c.tool, tool)
			assert.True(t, llmusage.ValidCategory(category), "category %q", category)
		})
	}
}

func TestCategorize_UnknownHarnessFallsToOther(t *testing.T) {
	category, tool := llmusage.Categorize("codex", "Bash")
	assert.Equal(t, llmusage.CategoryOther, category)
	assert.Equal(t, "Bash", tool)
}

func TestValidCategory(t *testing.T) {
	cases := []struct {
		category string
		want     bool
	}{
		{"bash", true},
		{"file_read", true},
		{"file_edit", true},
		{"search", true},
		{"web", true},
		{"subagent", true},
		{"other", true},
		{"mcp:slop-haus", true},
		{"mcp:a_B-9", true},
		{"", false},
		{"mcp:", false},
		{"MCP:x", false},
		{"bash2", false},
		{"mcp:a b", false},
	}
	for _, c := range cases {
		t.Run(c.category, func(t *testing.T) {
			assert.Equal(t, c.want, llmusage.ValidCategory(c.category))
		})
	}
}
