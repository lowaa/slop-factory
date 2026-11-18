// splitPaths turns a textarea's newline-separated scopes into a path list --
// normalization proper (repo-relative, no ".."/duplicate slashes, trailing
// "/" for directories) is server/internal/ticket/scope/paths.go's NormalizePath's
// job, not duplicated here. This only trims each line and drops blank ones,
// so an accidental empty line in the textarea doesn't turn into a spurious
// path scope the server would reject.
export function splitPaths(raw: string): string[] {
  return raw
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}
