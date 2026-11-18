// Captures CSS rule usage over some pages of a running site, written as a
// capture file for report.mjs. For a site that is not a Storybook; the
// stories have collect_stories.mjs.
//
//   collect_pages.mjs --out <capture.json> --viewports <config.json> [--scheme light]... [--workers <n>] <url>...
import { readFileSync, writeFileSync } from "node:fs";
import { parseArgs } from "node:util";
import { collectPages } from "./lib/collect.mjs";

const { values, positionals } = parseArgs({
  options: { out: { type: "string" }, viewports: { type: "string" }, scheme: { type: "string", multiple: true }, workers: { type: "string" } },
  allowPositionals: true,
});
if (!values.out || !values.viewports || positionals.length === 0) {
  console.error("usage: collect_pages.mjs --out <capture.json> --viewports <config.json> [--scheme light]... [--workers <n>] <url>...");
  process.exit(2);
}
const workers = values.workers === undefined ? undefined : Number(values.workers);
if (workers !== undefined && !(Number.isInteger(workers) && workers >= 1)) {
  console.error("collect_pages: --workers needs a whole number of at least 1");
  process.exit(2);
}
const { viewports } = JSON.parse(readFileSync(values.viewports, "utf8"));
const { recorder, failures } = await collectPages({
  urls: positionals,
  schemes: values.scheme?.length ? values.scheme : ["light"],
  viewports,
  workers,
  ready: (page) => page.waitForLoadState("networkidle"),
});
writeFileSync(values.out, JSON.stringify(recorder));
process.exit(failures.length ? 1 : 0);
