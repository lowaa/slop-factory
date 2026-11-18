import type { ReviewRun } from "@/api";

export interface HeadGroup {
  headSha: string;
  runs: ReviewRun[];
}

const SHORT_SHA_LENGTH = 7;

export function shortSha(sha: string): string {
  return sha.slice(0, SHORT_SHA_LENGTH);
}

// The attempt's signals may carry an abbreviated sha where a run carries the
// full one (or the reverse), so either being a prefix of the other counts.
// Git's own shortest abbreviation is 4: anything shorter would match heads at
// random.
const MIN_SHA_LENGTH = 4;

export function sameHead(a: string | null | undefined, b: string): boolean {
  if (!a || a.length < MIN_SHA_LENGTH || b.length < MIN_SHA_LENGTH) return false;
  return a.startsWith(b) || b.startsWith(a);
}

// Runs arrive newest first, so a head's group is placed where its newest run
// is: the head under review now comes first, however many runs an older one
// collected.
export function groupByHead(runs: ReviewRun[]): HeadGroup[] {
  const groups = new Map<string, HeadGroup>();
  for (const run of runs) {
    const group = groups.get(run.head_sha) ?? { headSha: run.head_sha, runs: [] };
    group.runs.push(run);
    groups.set(run.head_sha, group);
  }
  return [...groups.values()];
}

export function describeVerdict(verdict: string): string {
  return verdict.replaceAll("_", " ");
}
