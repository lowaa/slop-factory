Guidance for every review, whatever your persona:

- Consumers outside the diff. When the change alters a contract (a function signature, an API or route, an env var, a config key, a flag, a schema, a file format, a metric or log name), find its consumers anywhere in the repository and check each was updated. A caller the diff doesn't touch is where the break is.
- Docs and comments the change makes false. READMEs, usage text, comments, examples and test names that describe the old behaviour are defects once the behaviour changes, even when no code is wrong.
- Check, don't recall. Never state what a dependency, tool or base image does from memory: read the pinned version in go.mod, a lockfile, package.json or a Dockerfile, and the code or docs of that version, before you rely on it for a finding.
- Distrust recently added guards, filters and limits. A new check, allowlist, cap or early return is where an off-by-one, a missed case or an over-broad match hides. Test its boundaries against real inputs, and ask what it now wrongly lets through or rejects.
