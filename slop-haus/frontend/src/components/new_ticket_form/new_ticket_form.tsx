import { useEffect, useId, useState, type FormEvent } from "react";
import { apiClient, type AgentRole, type Priority, type RoleMatch } from "@/api";
import { Dropdown, Tooltip } from "@slop-factory/ui";
import { splitPaths } from "@/lib/split_paths";
import { useOnboardingStatus } from "@/components/onboarding";
import { AttachmentField, useAttachmentUploader } from "@/components/attachments";
import { RoleRequirementPicker } from "@/components/role_requirement_picker";
import { useUsage } from "@/usage/use_usage";
import "./new_ticket_form.scss";
import { errorMessage } from "@/lib/use_async_action";

interface NewTicketFormProps {
  onCreated: (ticketId: number) => void;
  // heading names the form; "File a ticket" when omitted.
  heading?: string;
  // defaults prefill the title and body, and are what the form goes back to
  // after each ticket it files -- a follow-up form starts from the ticket it
  // follows up on.
  defaults?: { title: string; body: string };
}

const PRIORITIES: Priority[] = ["low", "normal", "high", "urgent"];
const PRIORITY_OPTIONS = PRIORITIES.map((value) => ({ value, label: value }));

const NO_DEFAULTS = { title: "", body: "" };

// What each submit button files a ticket as. A tooltip over each on a
// mouse; on a touchscreen, where tapping the button is already the click,
// the same words show under the buttons instead.
const PREPARING_HINT = "Saved for you to keep refining -- no agent will pick it up until you open it.";
const OPEN_HINT = "Open now -- the next free agent may pick it up.";

export default function NewTicketForm({
  onCreated,
  heading = "File a ticket",
  defaults = NO_DEFAULTS,
}: NewTicketFormProps) {
  // Collapsed by default so the board, not the form, is what a visitor
  // sees first. Filing a ticket leaves this alone: someone filing several
  // in a row should not have to reopen the form after each one.
  const [expanded, setExpanded] = useState(false);
  const usage = useUsage();
  const fieldsId = useId();
  const [title, setTitle] = useState(defaults.title);
  const [body, setBody] = useState(defaults.body);
  const [priority, setPriority] = useState<Priority>("normal");
  const [pathsText, setPathsText] = useState("");
  const [baseRef, setBaseRef] = useState("");
  // Off by default -- most tickets are specific enough to build from
  // directly, and asking every agent to pause and check in first would
  // slow those down for no reason.
  const [needsDiscussion, setNeedsDiscussion] = useState(false);
  // Off by default -- most tickets merge as soon as an agent finishes them,
  // and asking a human to approve every PR would slow those down for no
  // reason. This is for the ones worth a second pair of eyes first.
  const [requiresApproval, setRequiresApproval] = useState(false);
  // The tenant's roles, fetched once for the picker below -- fetched even
  // while the form is collapsed, the same way the board's other panels
  // load their own data rather than waiting for a click to reveal it. A
  // tenant with no roles at all just never shows the picker (see roles.length
  // checks below); a failed fetch does the same rather than blocking filing
  // a ticket that needs no requirement.
  const [roles, setRoles] = useState<AgentRole[]>([]);
  // Tracked separately from roles being empty: a tenant that genuinely has
  // no roles should show nothing (below), but a fetch that merely failed
  // shouldn't look the same -- that reads as "nothing to require" when the
  // truth is "couldn't find out".
  const [rolesLoadFailed, setRolesLoadFailed] = useState(false);
  const [roleIds, setRoleIds] = useState<string[]>([]);
  const [roleMatch, setRoleMatch] = useState<RoleMatch>("any");
  useEffect(() => {
    let cancelled = false;
    apiClient
      .listAgentRoles()
      .then((loaded) => {
        if (!cancelled) setRoles(loaded);
      })
      .catch(() => {
        if (!cancelled) setRolesLoadFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  const onboarding = useOnboardingStatus();
  const [error, setError] = useState<string | null>(null);
  const uploader = useAttachmentUploader();
  // The ticket the files are still to be uploaded to, when filing it
  // succeeded and some of them did not: the buttons then retry those files
  // rather than filing a second ticket.
  const [filedId, setFiledId] = useState<number | null>(null);
  // Which button is mid-submit, if any -- both are disabled while either
  // is, but only the one actually clicked shows its own progress text and
  // aria-busy, so a human watching the other button doesn't see it claim
  // work it isn't doing.
  const [submittingStatus, setSubmittingStatus] = useState<"preparing" | "open" | null>(null);

  async function submitTicket(status: "preparing" | "open") {
    setError(null);
    setSubmittingStatus(status);
    try {
      // A turned-away file would be dropped unseen by a send that skipped it.
      if (uploader.items.some((item) => item.rejected)) {
        setError("Remove the attachments that cannot be sent first.");
        return;
      }
      if (filedId !== null) {
        if (!(await uploadAttachments(filedId))) return;
        setFiledId(null);
        uploader.clear();
        return;
      }
      const ticket = await apiClient.createTicket({
        title: title.trim(),
        body: body.trim(),
        priority,
        paths: splitPaths(pathsText).map((path) => ({ path })),
        needs_discussion: needsDiscussion,
        requires_approval: requiresApproval,
        status,
        ...(baseRef.trim() && { base_ref: baseRef.trim() }),
        ...(roleIds.length > 0 && { role_ids: roleIds, role_match: roleMatch }),
      });
      usage.event("new_ticket", { action: status === "open" ? "create_open" : "create_preparing" });
      // The ticket must exist before a file can be attached to it, so the
      // files wait here until now.
      if (!(await uploadAttachments(ticket.id))) {
        setFiledId(ticket.id);
        resetFields();
        onCreated(ticket.id);
        return;
      }
      resetFields();
      uploader.clear();
      onCreated(ticket.id);
    } catch (err) {
      setError(errorMessage(err, "Something went wrong. Please try again."));
    } finally {
      setSubmittingStatus(null);
    }
  }

  async function uploadAttachments(ticketId: number): Promise<boolean> {
    const sent = await uploader.uploadAll((file, { onProgress }) =>
      apiClient.uploadTicketAttachment(ticketId, file, { onProgress }),
    );
    if (!sent) setError(`Ticket #${ticketId} was filed, but some attachments did not upload. Remove them, or press Preparing or Open to try them again.`);
    return sent;
  }

  function resetFields() {
    setTitle(defaults.title);
    setBody(defaults.body);
    setPriority("normal");
    setPathsText("");
    setBaseRef("");
    setNeedsDiscussion(false);
    setRequiresApproval(false);
    setRoleIds([]);
      setRoleMatch("any");
  }

  // Both buttons are real submit buttons, so the title's `required` still
  // blocks an empty ticket and Enter still files one. Which status is read
  // off the button that submitted; Enter submits with the first, Preparing,
  // so the keyboard path takes the safe default rather than the queue.
  function handleFormSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const submitter = (e.nativeEvent as SubmitEvent).submitter;
    void submitTicket(submitter?.getAttribute("value") === "open" ? "open" : "preparing");
  }

  return (
    <form className="new-ticket-form" onSubmit={handleFormSubmit}>
      {/* The toggle sits inside the heading so the heading keeps its
          name and its place in the document outline. */}
      <h2>
        <button
          type="button"
          className="new-ticket-form__toggle"
          aria-expanded={expanded}
          aria-controls={fieldsId}
          onClick={() => {
            if (!expanded) usage.event("new_ticket", { action: "open_form" });
            setExpanded(!expanded);
          }}
        >
          <span className="new-ticket-form__chevron" aria-hidden="true" />
          {heading}
        </button>
      </h2>

      {/* Hidden rather than unmounted, so the toggle's aria-controls
          always points at an element that exists. */}
      <div id={fieldsId} className="new-ticket-form__fields" hidden={!expanded}>
        {onboarding && !onboarding.code_work_ready && (
          <p className="new-ticket-form__warning" role="note">
            This tenant has no repo and no code host yet, so no worker can do code work. You can file the ticket, but nothing will
            pick it up until setup is done.
          </p>
        )}
        <label className="new-ticket-form__field">
          <span>Title</span>
          {/* Not required while only the files of a ticket already filed are
              left to send: the fields were cleared when it was filed. */}
          <input type="text" required={filedId === null} value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>

        <div className="new-ticket-form__field">
          <AttachmentField uploader={uploader} disabled={submittingStatus !== null}>
            <label className="new-ticket-form__field">
              <span>Body</span>
              <textarea rows={4} value={body} onChange={(e) => setBody(e.target.value)} />
            </label>
          </AttachmentField>
        </div>

        <div className="new-ticket-form__field">
          <Dropdown
            label="Priority"
            value={priority}
            options={PRIORITY_OPTIONS}
            onChange={(value) => setPriority(value)}
          />
        </div>

        <label className="new-ticket-form__field">
          <span>Path scopes (one per line)</span>
          <textarea
            rows={3}
            placeholder={"server/internal/ticket/\nfrontend/src/components/board/"}
            value={pathsText}
            onChange={(e) => setPathsText(e.target.value)}
          />
        </label>

        <details className="new-ticket-form__field">
          <summary>Advanced</summary>
          <label className="new-ticket-form__field">
            <span>Base ref (commit hash or tag, not a branch)</span>
            <input
              type="text"
              placeholder="the base branch's tip when claimed"
              value={baseRef}
              onChange={(e) => setBaseRef(e.target.value)}
            />
          </label>
        </details>

        <RoleRequirementPicker
          roles={roles}
          selectedIds={roleIds}
          onSelectionChange={setRoleIds}
          matchMode={roleMatch}
          onMatchModeChange={setRoleMatch}
          legend="Roles needed to claim this"
          emptyHint={rolesLoadFailed ? "Couldn't load your tenant's roles -- try reloading the page." : undefined}
        />

        <div className="new-ticket-form__field new-ticket-form__field--checkbox">
          <label className="new-ticket-form__checkbox-row">
            <input type="checkbox" checked={needsDiscussion} onChange={(e) => setNeedsDiscussion(e.target.checked)} />
            <span>Needs discussion</span>
          </label>
          <span className="new-ticket-form__hint">
            The body is a direction, not a spec -- the first agent to pick this up will plan and ask before building.
          </span>
        </div>

        <div className="new-ticket-form__field new-ticket-form__field--checkbox">
          <label className="new-ticket-form__checkbox-row">
            <input
              type="checkbox"
              checked={requiresApproval}
              onChange={(e) => setRequiresApproval(e.target.checked)}
            />
            <span>Requires human PR approval</span>
          </label>
          <span className="new-ticket-form__hint">
            The agent opens a PR and waits for you to approve it before merging.
          </span>
        </div>

        {error && (
          <p className="new-ticket-form__error" role="alert">
            {error}
          </p>
        )}

        <div className="new-ticket-form__submit-row">
          {/* Preparing is the primary action here: it's the
            safer default -- a ticket a human is still shaping stays out of
            the queue until they say otherwise, rather than an agent
            possibly picking up something half-written a moment after it's
            filed. */}
          {/* aria-label pins the accessible name to "Preparing"/"Open" even
            while the visible text below says "Filing…" -- a screen reader
            user should not have the button they just activated seem to
            rename itself mid-click, and a test asking for it by that name
            should not have to guess which state it caught it in. */}
          <Tooltip text={PREPARING_HINT} hoverDelayMs={0}>
            <button
              type="submit"
              value="preparing"
              className="new-ticket-form__submit new-ticket-form__submit--primary"
              disabled={submittingStatus !== null}
              aria-busy={submittingStatus === "preparing"}
              aria-label="Preparing"
            >
              {submittingStatus === "preparing" ? "Filing…" : "Preparing"}
            </button>
          </Tooltip>
          <Tooltip text={OPEN_HINT} hoverDelayMs={0}>
            <button
              type="submit"
              value="open"
              className="new-ticket-form__submit"
              disabled={submittingStatus !== null}
              aria-busy={submittingStatus === "open"}
              aria-label="Open"
            >
              {submittingStatus === "open" ? "Filing…" : "Open"}
            </button>
          </Tooltip>
        </div>
        {/* Not aria-hidden: a phone's screen reader moves by swiping, which
            need not fire the focus the tooltips open on. Where the
            tooltips do work (a mouse), this is display: none and unread. */}
        <div className="new-ticket-form__submit-hints">
          <p>
            <strong>Preparing:</strong> {PREPARING_HINT}
          </p>
          <p>
            <strong>Open:</strong> {OPEN_HINT}
          </p>
        </div>
      </div>
    </form>
  );
}
