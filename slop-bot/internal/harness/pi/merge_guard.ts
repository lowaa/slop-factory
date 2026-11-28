// slop-bot's merge guard for pi, loaded with -e. Before each bash (or MCP)
// tool call it runs slop-bot's pre-tool hook, the command named by
// SLOP_BOT_PRE_TOOL_HOOK (a JSON argv), with the call as a Claude Code
// PreToolUse payload, and blocks the call when the hook refuses it.
//
// slop-bot writes this file at each start; edits to it don't last.
import { execFile } from "node:child_process";

const argv: string[] = JSON.parse(process.env.SLOP_BOT_PRE_TOOL_HOOK ?? "[]");

// Only for when the hook itself can't run: refuse what may be a merge,
// allow the rest, as the hook does when slop-bot's daemon is down.
const mayMerge = /\b(merge|push)/i;

type HookRun = { stdout: string; error?: Error };

function runHook(payload: string): Promise<HookRun> {
	return new Promise((resolve) => {
		const child = execFile(argv[0], argv.slice(1), { timeout: 30_000, maxBuffer: 1 << 20 }, (error, stdout) => {
			resolve({ stdout: String(stdout), error: error ?? undefined });
		});
		child.stdin?.end(payload);
	});
}

export default function (pi: any) {
	if (argv.length === 0) {
		return;
	}
	pi.on("tool_call", async (event: any) => {
		const tool = String(event.toolName ?? "");
		if (tool !== "bash" && !tool.startsWith("mcp")) {
			return undefined;
		}
		const command = typeof event.input?.command === "string" ? event.input.command : "";
		const payload = JSON.stringify({
			hook_event_name: "PreToolUse",
			tool_name: tool,
			tool_input: { command },
			cwd: process.cwd(),
		});
		const run = await runHook(payload);
		if (run.error) {
			if (mayMerge.test(tool === "bash" ? command : tool)) {
				return { block: true, reason: `slop-bot's merge guard couldn't run (${run.error.message}), so a call that may merge is refused` };
			}
			return undefined;
		}
		if (run.stdout.trim() === "") {
			return undefined;
		}
		try {
			const decision = JSON.parse(run.stdout)?.hookSpecificOutput;
			if (decision?.permissionDecision === "deny") {
				return { block: true, reason: String(decision.permissionDecisionReason ?? "refused by slop-bot's merge guard") };
			}
		} catch {
			return { block: true, reason: "slop-bot's merge guard gave an answer it couldn't read, so the call is refused" };
		}
		return undefined;
	});
}
