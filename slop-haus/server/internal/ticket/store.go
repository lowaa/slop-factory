package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// PostgresStore is the Store implementation. It holds SQL and nothing else:
// every rule about what a valid transition is lives in Service.
type PostgresStore struct {
	db *db.DB
	// settled is told which tickets each transition moved; see WithSettledHook.
	settled SettledHook
	// entered is told of the discussion rounds each transaction opened; see
	// discussionNotifier.
	entered func(ctx context.Context, entries []*DiscussionEntry)
	// workspace is where the shared paths come from; nil for none.
	workspace Workspace
	// mergeQueue removes landing rows; see MergeQueue.
	mergeQueue MergeQueue
}

// StoreOption configures a PostgresStore.
type StoreOption func(*PostgresStore)

// WithSettledHook tells h which tickets each committed transaction moved.
func WithSettledHook(h SettledHook) StoreOption {
	return func(s *PostgresStore) { s.settled = h }
}

func (s *PostgresStore) onDiscussion(f func(ctx context.Context, entries []*DiscussionEntry)) {
	s.entered = f
}

// WithStoreWorkspace tells the store where the shared paths come from, which
// its claims and extensions read in their own transactions. Without it no
// path is shared and every overlap blocks.
func WithStoreWorkspace(w Workspace) StoreOption {
	return func(s *PostgresStore) { s.workspace = w }
}

// WithMergeQueue replaces the landing queue's side of the store, which is
// landing.Dequeuer in the server. Without it a Dequeue removes nothing.
func WithMergeQueue(q MergeQueue) StoreOption {
	return func(s *PostgresStore) { s.mergeQueue = q }
}

func NewPostgresStore(database *db.DB, opts ...StoreOption) *PostgresStore {
	s := &PostgresStore{db: database, mergeQueue: noMergeQueue{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) CreateTicket(ctx context.Context, params *CreateParams) (*Ticket, error) {
	var id int64
	err := s.db.InTx(ctx, func(tx db.Querier) error {
		var err error
		id, err = insertTicket(ctx, tx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetTicket(ctx, id)
}

// insertTicket files a ticket, with its roles, paths and blockers, in tx --
// a transaction of the caller's, so a caller can write more with it.
func insertTicket(ctx context.Context, tx db.Querier, params *CreateParams) (int64, error) {
	// Only a ticket filed with blockers writes edges, which need the
	// claim lock; the common case doesn't queue behind every claim.
	if len(params.BlockedOn) > 0 {
		if err := db.TakeAdvisoryLock(ctx, tx, claimAdvisoryKey); err != nil {
			return 0, err
		}
	}
	filer := Author{AgentID: params.CreatedByAgentID, UserID: params.CreatedByUserID}
	if err := authorMover(filer, "created").set(ctx, tx); err != nil {
		return 0, err
	}
	var intakeSource *uuid.UUID
	var intakeKey, intakeTitle *string
	if params.Intake != nil {
		intakeSource, intakeKey, intakeTitle = &params.Intake.SourceID, &params.Intake.Key, &params.Intake.Title
	}
	var id int64
	baseRef := ""
	if params.baseCommit != "" {
		baseRef = params.BaseRef
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO tickets (title, body, priority, status, created_by_agent_id, created_by_user_id,
		                     needs_discussion, requires_approval, role_match, intake_source_id, intake_key, intake_title,
		                     base_ref, base_commit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), NULLIF($14, ''))
		RETURNING id`,
		params.Title, params.Body, string(params.Priority), string(params.Status),
		params.CreatedByAgentID, params.CreatedByUserID, params.NeedsDiscussion, params.RequiresApproval,
		string(params.Roles.matchOrDefault()), intakeSource, intakeKey, intakeTitle, baseRef, params.baseCommit)
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("insert ticket: %w", err)
	}
	// In the same transaction as the insert, so a ticket filed as open
	// is never claimable without the roles it was filed with.
	if !params.Roles.empty() {
		if err := writeRequiredRoles(ctx, tx, id, &params.Roles); err != nil {
			return 0, err
		}
	}
	if err := insertPaths(ctx, tx, id, params.Paths); err != nil {
		return 0, err
	}
	for _, blocker := range params.BlockedOn {
		if _, err := addBlocker(ctx, tx, filer, id, blocker, ""); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// insertPaths adds paths to the ticket. One it already has keeps its row
// and source: an edit naming an extended path it kept leaves that path
// extended, so a client sending back the scope it read cannot turn an
// agent's grant into a filed path the next edit would drop.
func insertPaths(ctx context.Context, q db.Querier, ticketID int64, paths []model.Path) error {
	for _, p := range paths {
		_, err := q.Exec(ctx, `
			INSERT INTO ticket_paths (ticket_id, path, source)
			VALUES ($1, $2, $3) ON CONFLICT (tenant_id, ticket_id, path) DO NOTHING`,
			ticketID, p.Path, string(p.Source))
		if err != nil {
			return fmt.Errorf("insert path %q: %w", p.Path, err)
		}
	}
	return nil
}

// TicketFrom is the FROM of every read of a ticket: the ticket and its
// attempt, the promoted one (attempts_one_promoted). The seven fields that
// moved to attempts -- resume_agent_id, affinity_until, pr_url, approved_at,
// approved_by_user_id, changes_requested_at, review_started_at -- are read
// as pa.<field> and nowhere else, so what "the ticket's attempt" means is
// defined here once. A ticket with no promoted attempt (possible once a
// ticket has several) reads them as NULL until one is chosen.
const TicketFrom = `tickets t ` + AttemptJoin

// promotedAttemptHas is a predicate on a ticket t: its promoted attempt, as
// pp, satisfies cond. Filters use it rather than TicketFrom's join so they
// work in any query over tickets t, such as the subqueries of other stores.
func promotedAttemptHas(cond string) string {
	return `EXISTS (SELECT 1 FROM attempts pp WHERE pp.tenant_id = t.tenant_id AND pp.ticket_id = t.id
		AND pp.promoted_at IS NOT NULL AND ` + cond + `)`
}

// AttemptJoin joins the promoted attempt as pa to a ticket already in scope as
// t, for a query whose FROM is not TicketFrom's (a ticket reached through a
// lease, say).
const AttemptJoin = `LEFT JOIN attempts pa ON pa.tenant_id = t.tenant_id AND pa.ticket_id = t.id AND pa.promoted_at IS NOT NULL`

var ticketColumns = `t.id, t.title, t.body, t.priority, t.status, t.created_by_agent_id, t.created_by_user_id, t.created_by_user_id_deleted,
	t.needs_discussion, pa.resume_agent_id, pa.affinity_until, t.revision, pa.pr_url, t.base_commit, t.base_ref,
	t.requires_approval, pa.approved_at, pa.approved_by_user_id, COALESCE(pa.approved_by_user_id_deleted, false), pa.changes_requested_at, pa.review_started_at,
	t.post_reviewed_at, t.post_reviewed_by_user_id, t.post_reviewed_by_user_id_deleted,
	t.created_at, t.updated_at, t.closed_at, t.role_match, t.intake_source_id, t.intake_key, t.intake_title,
	t.blocked_until, t.cooldown_releases, t.cooldown_since, ` + noEligibleAgent

// summaryColumns is ticketColumns without the body, for lists of cards.
var summaryColumns = strings.Replace(ticketColumns, " t.body,", " '' AS body,", 1)

// scanTicket reads a row of ticketColumns or summaryColumns, then the sort's
// computed column when the query selected one.
func scanTicket(row pgx.Row, computed ...func(*Ticket) any) (*Ticket, error) {
	var t Ticket
	var priority, status, roleMatch string
	var intakeSource *uuid.UUID
	var intakeKey, intakeTitle *string
	dest := []any{&t.ID, &t.Title, &t.Body, &priority, &status,
		&t.CreatedByAgentID, &t.CreatedByUserID, &t.CreatedByUserDeleted, &t.NeedsDiscussion, &t.ResumeAgentID, &t.AffinityUntil, &t.Revision, &t.PRURL, &t.BaseCommit, &t.BaseRef,
		&t.RequiresApproval, &t.ApprovedAt, &t.ApprovedByUserID, &t.ApprovedByDeleted, &t.ChangesRequestedAt, &t.ReviewStartedAt,
		&t.PostReviewedAt, &t.PostReviewedByUserID, &t.PostReviewedByDeleted,
		&t.CreatedAt, &t.UpdatedAt, &t.ClosedAt, &roleMatch, &intakeSource, &intakeKey, &intakeTitle,
		&t.BlockedUntil, &t.CooldownReleases, &t.CooldownSince, &t.NoEligibleAgent}
	for _, c := range computed {
		// A hook for several columns (the attempt card's) returns them all.
		switch d := c(&t).(type) {
		case []any:
			dest = append(dest, d...)
		default:
			dest = append(dest, d)
		}
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	t.Priority = model.Priority(priority)
	t.Status = Status(status)
	t.RoleMatch = model.RoleMatch(roleMatch)
	if intakeSource != nil && intakeKey != nil {
		t.Intake = &model.IntakeRef{SourceID: *intakeSource, Key: *intakeKey}
		if intakeTitle != nil {
			t.Intake.Title = *intakeTitle
		}
	}
	return &t, nil
}

func (s *PostgresStore) GetTicket(ctx context.Context, id int64) (*Ticket, error) {
	return loadTicket(ctx, s.db, id)
}

// loadTicket reads a ticket and its scopes and dependencies through any
// Querier, so the claim path can return the ticket it just granted from
// inside the same transaction rather than re-reading it afterwards (where
// another claimer could have changed it in between).
func loadTicket(ctx context.Context, q db.Querier, id int64) (*Ticket, error) {
	row := q.QueryRow(ctx, `SELECT `+ticketColumns+` FROM `+TicketFrom+` WHERE t.id = $1`, id)

	t, err := scanTicket(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get ticket: %w", err)
	}

	if t.Paths, err = pathsFor(ctx, q, id); err != nil {
		return nil, err
	}
	if t.Blockers, err = depsFor(ctx, q, id); err != nil {
		return nil, err
	}
	t.DependsOn = blockerIDs(t.Blockers)
	if t.RequiredRoles, err = requiredRolesFor(ctx, q, id); err != nil {
		return nil, err
	}
	return t, nil
}

func pathsFor(ctx context.Context, q db.Querier, ticketID int64) ([]model.Path, error) {
	rows, err := q.Query(ctx,
		`SELECT path, source FROM ticket_paths WHERE ticket_id = $1 ORDER BY path`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	defer rows.Close()

	paths := []model.Path{}
	for rows.Next() {
		var p model.Path
		var source string
		if err := rows.Scan(&p.Path, &source); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		p.Source = model.PathSource(source)
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

func depsFor(ctx context.Context, q db.Querier, ticketID int64) ([]model.TicketRef, error) {
	rows, err := q.Query(ctx, `
		SELECT d.depends_on_ticket_id, b.title, b.status FROM ticket_deps d
		JOIN tickets b ON b.tenant_id = d.tenant_id AND b.id = d.depends_on_ticket_id
		WHERE d.ticket_id = $1 ORDER BY d.depends_on_ticket_id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list deps: %w", err)
	}
	defer rows.Close()

	deps := []model.TicketRef{}
	for rows.Next() {
		var ref model.TicketRef
		var status string
		if err := rows.Scan(&ref.ID, &ref.Title, &status); err != nil {
			return nil, fmt.Errorf("scan dep: %w", err)
		}
		ref.Status = Status(status)
		deps = append(deps, ref)
	}
	return deps, rows.Err()
}

func blockerIDs(refs []model.TicketRef) []int64 {
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// ListTickets loads the matching tickets, then their paths and dependencies
// in one query each rather than per ticket -- the board renders every
// ticket's scopes, so the N+1 shape would be the whole page's cost.
func (s *PostgresStore) ListTickets(ctx context.Context, filter *ListFilter) ([]Ticket, error) {
	return listTickets(ctx, s.db, filter)
}

// listQuery is the SQL of one read of a list: the filter's predicate, the
// keyset past its cursor, its sort and its limit.
func listQuery(filter *ListFilter) (string, []any, error) {
	columns := ticketColumns
	if filter.Summary {
		columns = summaryColumns
	}
	if c := filter.sort().Column; c != nil {
		columns += ", " + c.SQL
	}
	from := TicketFrom
	if filter.AttemptCards {
		columns += cardColumns
		from = cardFrom(filter)
	}
	where, args := ticketPredicate(filter, 0)
	branches := []string{where}
	if filter.After != nil {
		after, afterArgs, err := afterBranches(filter, filter.After, len(args))
		if err != nil {
			return "", nil, err
		}
		branches = branches[:0]
		for _, a := range after {
			branches = append(branches, where+` AND `+a)
		}
		args = append(args, afterArgs...)
	}
	sort := filter.listSort()
	order := sort.orderBy()
	limit := ""
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		limit = fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	if len(branches) == 1 {
		return `SELECT ` + columns + ` FROM ` + from + ` WHERE ` + branches[0] + ` ORDER BY ` + order + limit, args, nil
	}
	// Each branch is an ordered index range and needs no more than a page, so
	// the outer query merges them without sorting the tickets.
	selects := make([]string, len(branches))
	for i, b := range branches {
		selects[i] = `(SELECT ` + columns + ` FROM ` + from + ` WHERE ` + b + ` ORDER BY ` + order + limit + `)`
	}
	return `SELECT * FROM (` + strings.Join(selects, ` UNION ALL `) + `) u ORDER BY ` + sort.orderByOf("u") + limit, args, nil
}

// listTickets takes a Querier so the graph can read it inside the same
// snapshot as the edges it will be joined against.
func listTickets(ctx context.Context, q db.Querier, filter *ListFilter) ([]Ticket, error) {
	query, args, err := listQuery(filter)
	if err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()

	var computed []func(*Ticket) any
	if c := filter.sort().Column; c != nil {
		computed = append(computed, c.Dest)
	}
	var card cardDest
	if filter.AttemptCards {
		computed = append(computed, func(*Ticket) any { return card.dest() })
	}
	tickets := []Ticket{}
	ids := []int64{}
	for rows.Next() {
		card = cardDest{}
		t, err := scanTicket(rows, computed...)
		if err != nil {
			return nil, fmt.Errorf("scan ticket: %w", err)
		}
		if filter.AttemptCards {
			t.Card = card.finish()
		}
		t.Paths = []model.Path{}
		t.DependsOn = []int64{}
		t.Blockers = []model.TicketRef{}
		t.RequiredRoles = []model.RoleRef{}
		tickets = append(tickets, *t)
		ids = append(ids, t.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tickets) == 0 {
		return tickets, nil
	}

	byID := make(map[int64]*Ticket, len(tickets))
	for i := range tickets {
		byID[tickets[i].ID] = &tickets[i]
	}
	if err := attachPaths(ctx, q, ids, byID); err != nil {
		return nil, err
	}
	if err := attachDeps(ctx, q, ids, byID); err != nil {
		return nil, err
	}
	if err := attachRequiredRoles(ctx, q, ids, byID); err != nil {
		return nil, err
	}
	return tickets, nil
}

func attachPaths(ctx context.Context, q db.Querier, ids []int64, byID map[int64]*Ticket) error {
	rows, err := q.Query(ctx, `
		SELECT ticket_id, path, source FROM ticket_paths
		WHERE ticket_id = ANY($1) ORDER BY ticket_id, path`, ids)
	if err != nil {
		return fmt.Errorf("list paths: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ticketID int64
		var p model.Path
		var source string
		if err := rows.Scan(&ticketID, &p.Path, &source); err != nil {
			return fmt.Errorf("scan path: %w", err)
		}
		p.Source = model.PathSource(source)
		if t, ok := byID[ticketID]; ok {
			t.Paths = append(t.Paths, p)
		}
	}
	return rows.Err()
}

func attachDeps(ctx context.Context, q db.Querier, ids []int64, byID map[int64]*Ticket) error {
	rows, err := q.Query(ctx, `
		SELECT d.ticket_id, d.depends_on_ticket_id, b.title, b.status FROM ticket_deps d
		JOIN tickets b ON b.tenant_id = d.tenant_id AND b.id = d.depends_on_ticket_id
		WHERE d.ticket_id = ANY($1) ORDER BY d.ticket_id, d.depends_on_ticket_id`, ids)
	if err != nil {
		return fmt.Errorf("list deps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ticketID int64
		var ref model.TicketRef
		var status string
		if err := rows.Scan(&ticketID, &ref.ID, &ref.Title, &status); err != nil {
			return fmt.Errorf("scan dep: %w", err)
		}
		ref.Status = Status(status)
		if t, ok := byID[ticketID]; ok {
			t.DependsOn = append(t.DependsOn, ref.ID)
			t.Blockers = append(t.Blockers, ref)
		}
	}
	return rows.Err()
}

// updateFields writes the fields of params other than the status, which
// only a transition writes, in a transaction the caller owns -- see
// Service.Update.
func updateFields(ctx context.Context, tx db.Querier, id int64, params *UpdateParams) error {
	if params.Status != nil {
		return fmt.Errorf("ticket: #%d's status is written by a transition, not as a field", id)
	}
	sets := []string{"updated_at = now()"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", clause, len(args)))
	}
	if params.Priority != nil {
		add("priority", string(*params.Priority))
	}
	if params.NeedsDiscussion != nil {
		add("needs_discussion", *params.NeedsDiscussion)
	}
	if params.RequiresApproval != nil {
		add("requires_approval", *params.RequiresApproval)
	}
	if params.baseRef != nil {
		if err := writeBaseRef(ctx, tx, id, params.baseRef); err != nil {
			return err
		}
	}

	// id is appended last, so its placeholder number is len(args)
	// after the append -- the SET clauses took $1..$N in the order
	// `add` appended them. Placeholders bind positionally to args, not
	// to where they appear in the statement text, so this stays correct
	// as long as every value is appended in the same order its
	// placeholder is numbered.
	args = append(args, id)
	query := fmt.Sprintf("UPDATE tickets SET %s WHERE id = $%d",
		strings.Join(sets, ", "), len(args))

	res, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update ticket: %w", err)
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if params.NeedsDiscussion != nil && *params.NeedsDiscussion {
		if err := forgetDiscussions(ctx, tx, id); err != nil {
			return err
		}
	}

	if params.Paths != nil {
		// Only the filed paths: an extended one is an agent's grant for
		// work under way, and a person's edit taking it away would narrow
		// that agent's scope under it without its knowing.
		if _, err := tx.Exec(ctx,
			`DELETE FROM ticket_paths WHERE ticket_id = $1 AND source <> 'extended'`, id); err != nil {
			return fmt.Errorf("clear paths: %w", err)
		}
		if err := insertPaths(ctx, tx, id, params.Paths); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) AddComment(ctx context.Context, ticketID int64, by Author, body string) (*Comment, error) {
	var c *Comment
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		c, err = insertComment(ctx, q, ticketID, by, body)
		return err
	})
	return c, err
}

// requeuedStatus is where a ticket re-entering the queue lands, for an
// UPDATE on `tickets t`: open, or blocked while anything holds it -- an
// unfinished dependency, or a cooldown still running. Shared by every route
// that puts a ticket back without a person deciding to, so they cannot
// disagree about what "back in the queue" means.
var requeuedStatus = `CASE WHEN ` + isBlocked + ` THEN 'blocked' ELSE 'open' END`

// attemptHas is a predicate on a ticket t: one of its attempts, as da,
// satisfies cond.
func attemptHas(cond string) string {
	return `EXISTS (SELECT 1 FROM attempts da WHERE da.tenant_id = t.tenant_id AND da.ticket_id = t.id AND ` + cond + `)`
}

// derivedStatus is the status a ticket in the queue lifecycle shows, worked
// out from its attempts, for an UPDATE on `tickets t`: the one definition,
// shared by every write of a ticket's status that follows its attempts. The
// first row that matches wins, and anything waiting on a person outranks
// anything claimable: several checks read the ticket's status, not an
// attempt's (the scope a ticket in review holds, a person's review decision,
// the answer to a discussion), so a sibling's queued or running attempt must
// not hide an attempt in discussion or review. Claim therefore offers a
// ticket in any of those statuses (candidateTickets). The same holds for a
// promoted attempt in the landing queue, which outranks a sibling's running
// or queued attempt. With one attempt it gives that attempt's status.
//
// Statement-level reads see the row as it was before the statement, so it
// must run after the attempts it reads, and after the cooldown columns
// requeuedStatus reads, have been written: WriteTicket does the statuses in
// a statement of its own for that reason.
//
// NULL when nothing is live and none is promoted, which the status column
// refuses (see deriveStatus).
var derivedStatus = `CASE
	WHEN ` + attemptHas("da.promoted_at IS NOT NULL AND da.status = 'done'") + ` THEN 'done'
	WHEN ` + attemptHas("da.status = 'discussion'") + ` THEN 'discussion'
	WHEN ` + attemptHas("da.status = 'review'") + ` THEN 'review'
	WHEN ` + attemptHas("da.status = 'in_merge_queue'") + ` THEN 'in_merge_queue'
	WHEN ` + attemptHas("da.status = 'in_progress'") + ` THEN 'in_progress'
	WHEN ` + attemptHas("da.status = 'queued'") + ` THEN ` + requeuedStatus + `
END`

// inQueueLifecycle is true, for a row of `tickets t`, while its status is
// the one derivedStatus gives: a person's preparing, parked and cancelled are
// set directly, and what the attempts do meanwhile does not move them.
var inQueueLifecycle = `t.status IN ` + queueLifecycleStatusesSQL

// isBlocked is true, for a row of `tickets t`, while the ticket must stay out
// of the queue: the one definition of "blocked" every status write uses.
// Time is an input like any other, so a cooldown ending reopens a ticket
// exactly as its last dependency finishing does (see unblockSatisfied).
var isBlocked = `(` + hasUnfinishedDeps + ` OR ` + hasLiveCooldown + `)`

// hasLiveCooldown is true, for a row of `tickets t`, until its blocked_until.
const hasLiveCooldown = `(t.blocked_until IS NOT NULL AND t.blocked_until > now())`

// hasUnfinishedDeps is true, for a row of `tickets t`, while any of its
// dependencies is not finished.
var hasUnfinishedDeps = `EXISTS (
	    SELECT 1 FROM ticket_deps d
	      JOIN tickets dep ON dep.id = d.depends_on_ticket_id
	     WHERE d.ticket_id = t.id AND dep.status NOT IN ` + TerminalStatusesSQL + `)`

func (s *PostgresStore) ListExternal(ctx context.Context, ticketID int64) ([]ExternalEvent, error) {
	return listExternal(ctx, s.db, ticketID)
}

// ListAgentContexts returns a ticket's handoffs, oldest first: every attempt's.
func (s *PostgresStore) ListAgentContexts(ctx context.Context, ticketID int64) ([]AgentContext, error) {
	return s.ListAgentContextsIn(ctx, ticketID, EveryConversation())
}

// ListComments returns a ticket's comments, oldest first: every attempt's.
func (s *PostgresStore) ListComments(ctx context.Context, ticketID int64) ([]Comment, error) {
	return s.ListCommentsIn(ctx, ticketID, EveryConversation())
}

// dependsOnTransitively walks the dependency edges from `from` looking for
// `to`. UNION (not UNION ALL) is what terminates the walk on a graph that
// already contains a cycle -- it discards rows already in the working set,
// so a pre-existing cycle converges instead of recursing forever.
func dependsOnTransitively(ctx context.Context, q db.Querier, from, to int64) (bool, error) {
	row := q.QueryRow(ctx, `
		WITH RECURSIVE reachable(id) AS (
			SELECT depends_on_ticket_id FROM ticket_deps WHERE ticket_id = $1
			UNION
			SELECT d.depends_on_ticket_id
			  FROM ticket_deps d
			  JOIN reachable r ON d.ticket_id = r.id
		)
		SELECT EXISTS (SELECT 1 FROM reachable WHERE id = $2)`, from, to)

	var found bool
	if err := row.Scan(&found); err != nil {
		return false, fmt.Errorf("walk dependencies: %w", err)
	}
	return found, nil
}

// CountTickets counts the tickets matching filter in each status it names
// (every status when none), from the live-status index where it can. The
// filter's page fields are ignored. Of an attempt-card filter it counts the
// cards, in their attempts' statuses.
func (s *PostgresStore) CountTickets(ctx context.Context, filter *ListFilter) (map[Status]int, error) {
	if filter.AttemptCards {
		return s.countCards(ctx, filter)
	}
	where, args := ticketPredicate(filter, 0)
	rows, err := s.db.Query(ctx, `SELECT t.status, count(*) FROM tickets t WHERE `+where+` GROUP BY t.status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count tickets: %w", err)
	}
	defer rows.Close()
	counts := map[Status]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan ticket count: %w", err)
		}
		counts[Status(status)] = n
	}
	return counts, rows.Err()
}
