import "./workspace_help.scss";

// How workspaces work, for someone setting one up for the first time.
// Collapsed by default, like Add a machine's "Add a slot manually": the manifest
// and statuses below are what this screen is for, and this is read once.
export default function WorkspaceHelp() {
  return (
    <details className="workspace-help">
      <summary>How workspaces work</summary>

      <div className="workspace-help__body">
        <section>
          <h3>One root, the same for every agent</h3>
          <p>
            A workspace is the one root directory an agent works in, and every agent in the fleet has an identical
            copy of it. The root holds one checkout per
            repo &mdash; say <code>app/</code> and <code>deploy/</code> &mdash; so work can span a source repo and
            the repo that ships it.
          </p>
          <p>
            A ticket&rsquo;s path scopes are relative to the root, so <code>app/src/</code> and{" "}
            <code>deploy/src/</code> never collide.
          </p>
        </section>

        <section>
          <h3>What is reported</h3>
          <p>
            Each time an agent&rsquo;s session starts, and after every turn, its workspace tells slop-haus what is
            on disk: whether the root is a git repo and its <code>origin</code>, and the same for each top-level
            directory that is one. Nothing else is looked at &mdash; no listing of the root, nothing nested deeper
            &mdash; and a remote has any credential stripped from it before it leaves the machine
            (<code>https://user:token@host/x</code> is sent as <code>host/x</code>).
          </p>
          <p>
            Reports come from the hooks every start installs. A workspace whose agent has not been started
            since reporting was added reports nothing and shows as <strong>unknown</strong>: restart the
            agent there. Unknown never stops an agent working.
          </p>
        </section>

        <section>
          <h3>The manifest</h3>
          <p>
            The manifest says which repo is checked out at which path under the root; <code>.</code> means the root
            is the repo. A tenant starts with none, and while there is none nothing is enforced: agents still
            report, and this screen proposes a manifest from what they report, naming any agent that disagrees so
            you can fix it first. The tenant&rsquo;s owners adopt it, or write their own, here. Adopting changes
            nothing for tickets already open.
          </p>
          <p>
            Once there is a manifest, each agent shows as <strong>matches</strong> (every manifest path is a repo
            with the right origin), <strong>mismatch</strong> (this screen lists the path and what was found
            there), or <strong>unknown</strong> (it has not reported). A mismatch in the repo a ticket&rsquo;s scopes
            need makes the agent release the ticket when it is granted it, saying what it found. A mismatch in any
            other repo is only a warning here, and unknown never blocks.
          </p>
          <p>
            Remotes are compared after normalising them, so <code>git@github.com:a/b.git</code> equals{" "}
            <code>https://github.com/a/b</code> (scheme, user, <code>.git</code> suffix and the case of the host
            do not matter). Branches and commits are not compared, and directories the manifest does not list are
            ignored.
          </p>
        </section>

        <section>
          <h3>No Git credentials here</h3>
          <p>
            slop-haus never checks code out and holds no Git credentials. When a workspace is set up, and each time it
            is updated (every start does), it clones any manifest repo whose directory is <em>missing</em> using
            that machine&rsquo;s own Git access &mdash; the same credentials it already pushes with &mdash; over
            https, or over ssh if the repos already there were cloned that way (
            <code>SLOP_HAUS_CLONE_PROTOCOL=ssh</code> or <code>https</code> overrides). A clone that fails (no
            access from that machine, say) is reported and the rest carry on.
          </p>
          <p>
            It only ever adds. A directory that already exists is never moved, deleted, re-pointed or changed, even
            when its origin is wrong: it shows here as a mismatch and fixing it is up to you. The workspace
            root itself must not be a git repo: if it is one, that shows here as a mismatch.
          </p>
          <p>
            Cloning is optional. If you keep layouts in step another way (submodules, <code>repo</code>, a
            devcontainer), run setup or the updater once with <code>SLOP_HAUS_CLONE=no</code>: the choice is
            recorded in <code>.slop-haus/no-clone</code> in that workspace, every later update respects it, and
            delete that file to turn cloning back on. The layout is still reported and validated either way.
          </p>
        </section>

        <section>
          <h3>One ticket, one repo</h3>
          <p>
            A change across repos needs a pull request in each and cannot land atomically, so cross-repo work is
            one ticket per repo, ordered with blockers. Once there is a manifest, a ticket whose path scopes span
            repos is refused when it is filed or its scopes change, and its pull request must be in its repo.
          </p>
        </section>

        <section>
          <h3>Deploy-only files</h3>
          <p>
            slop-haus merges a ticket&rsquo;s pull request only once its checks have passed on a head that contains
            the base branch&rsquo;s latest commit. If CI commits deploy state to that branch &mdash; image tags in a
            kustomization, say &mdash; every deploy makes every waiting pull request take the branch in and run its
            checks again.
          </p>
          <p>
            A repo&rsquo;s deploy-only files are the exception. A pull request whose head is behind only by commits
            to those files merges as it is, provided it changes none of them itself and renames no file. Each entry is a file pattern
            relative to that repo&rsquo;s own root, where <code>*</code> and <code>?</code> do not cross a{" "}
            <code>/</code> (<code>deploy/*/kustomization.yaml</code>), and optionally a regular expression that every
            line a deploy adds or removes in such a file must match in full (<code>{"\\s*newTag: \\S+"}</code>). A
            deploy that changes any other file, or any other line, is not deploy-only, and the pull request takes the
            branch in as usual.
          </p>
          <p>
            Only list files no check reads differently when a deploy changes them. Image tags that the tests replace
            with the pull request&rsquo;s own images are the case this is for: the merged tree is then the tested
            change plus deploy state. A file a check does read this way would merge untested.
          </p>
        </section>

        <section>
          <h3>Shared paths</h3>
          <h4>What problem they solve</h4>
          <p>
            Each ticket declares the paths it will change, and slop-haus will not let two agents work on overlapping
            paths at once, so their changes cannot collide.
          </p>
          <p>
            Some paths are touched by many unrelated tickets, yet their collisions are mechanical: a directory of
            numbered database migrations (two tickets both add &ldquo;migration 83&rdquo;; the second renumbers to
            84), or generated code that is simply regenerated after merging. Blocking on these leaves agents idle for
            no benefit.
          </p>
          <p>
            A shared path says: overlaps here do not block. Agents work in parallel, and the later one resolves the
            clash when it merges main, the way the resolve kind says. The overlap is still shown, marked shared.
          </p>
          <p>
            <strong>The cost:</strong> a real conflict there is resolved by the agent, not prevented. List only paths
            whose clashes really are mechanical. Never list hand-written code, deploy state or anything whose merge
            needs judgement.
          </p>
          <h4>Writing a path</h4>
          <p>Paths are relative to that repo&rsquo;s own root.</p>
          <ul>
            <li>
              <strong>Ends in <code>/</code>: a directory, and everything under it.</strong>{" "}
              <code>server/db/migrations/</code> covers every file and subdirectory there. This is what you want for a
              migrations or generated directory.
            </li>
            <li>
              <strong>No trailing <code>/</code>: a file pattern.</strong> <code>*</code> and <code>?</code> match
              within one path segment and never cross a <code>/</code>. <code>web/api_generated/schema.d.ts</code>{" "}
              names one file; <code>docs/*.generated.md</code> names the matching files in that one directory.
            </li>
          </ul>
          <p>
            The common mistake is a directory written without its slash (<code>web/api_generated</code>). It is read
            as a file with that name, matches nothing a ticket lists, and silently does nothing. A file pattern never
            covers an overlap on a whole directory either: <code>migrations/*.sql</code> does not help when tickets
            list <code>migrations/</code>.
          </p>
          <p>
            An overlap is covered only if it lies wholly inside a shared path; overlapping on a shared path and
            anything else still blocks. With <code>db/migrations/</code> shared: two tickets that both list{" "}
            <code>db/migrations/</code> both run; one listing <code>db/migrations/</code> and another listing{" "}
            <code>db/</code> overlap only on the migrations directory, so they run too; two tickets that both list{" "}
            <code>db/</code> overlap on the rest of it, and block.
          </p>
          <h4>How a clash is resolved</h4>
          <ul>
            <li>
              <strong>migration</strong>: renumber your migration to follow what landed, update <code>LATEST</code>,
              and check it still makes sense after theirs.
            </li>
            <li>
              <strong>regenerate</strong>: re-run the generator after merging main, never hand-merge its output, then
              read the regenerated diff.
            </li>
            <li>
              <strong>merge</strong>: merge the base branch and resolve the conflict by hand, keeping both sides' intent. Re-run
              the tests that cover the file. If both sides changed the same behaviour, not just neighbouring lines,
              ask with <code>ticket_discuss</code> rather than guess.
            </li>
          </ul>
        </section>
      </div>
    </details>
  );
}
