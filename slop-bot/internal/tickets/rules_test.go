package tickets_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var (
	user   = ident.UserPrincipal()
	agent1 = ident.AgentPrincipal("a1", "1") // works ticket 1
	agent2 = ident.AgentPrincipal("a2", "2") // works ticket 2

	urgentOn  = &tickets.Config{AgentsMaySetUrgent: true}
	urgentOff = &tickets.Config{}
)

func ptr[T any](v T) *T { return &v }

// ownTicket is agent1's own ticket (id 1), in the given status.
func ownTicket(status tickets.Status) *tickets.Ticket {
	return &tickets.Ticket{ID: "1", Status: status, CreatedBy: user}
}

// filedBy1 is a ticket (id 9) that agent1 filed from ticket 1.
func filedBy1(status tickets.Status) *tickets.Ticket {
	return &tickets.Ticket{ID: "9", Status: status, CreatedBy: agent1, Parent: "1"}
}

func usersTicket(status tickets.Status) *tickets.Ticket {
	return &tickets.Ticket{ID: "9", Status: status, CreatedBy: user}
}

func assertErr(t *testing.T, want, got error) {
	t.Helper()
	if want == nil {
		assert.NoError(t, got)
		return
	}
	assert.ErrorIs(t, got, want)
}

func TestCheckPrincipal(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		by   ident.Principal
		want error
	}{
		"user":                 {user, nil},
		"agent":                {agent1, nil},
		"zero":                 {ident.Principal{}, tickets.ErrInvalidPrincipal},
		"unknown kind":         {ident.Principal{Kind: 3}, tickets.ErrInvalidPrincipal},
		"user with an agent":   {ident.Principal{Kind: ident.User, Agent: "a"}, tickets.ErrInvalidPrincipal},
		"user with a ticket":   {ident.Principal{Kind: ident.User, Ticket: "1"}, tickets.ErrInvalidPrincipal},
		"agent without ticket": {ident.Principal{Kind: ident.Agent, Agent: "a"}, tickets.ErrInvalidPrincipal},
		"agent without id":     {ident.Principal{Kind: ident.Agent, Ticket: "1"}, tickets.ErrInvalidPrincipal},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertErr(t, c.want, tickets.CheckPrincipal(c.by))
		})
	}
}

// Every Check* judges the principal first.
func TestEveryCheckRefusesAnInvalidPrincipal(t *testing.T) {
	t.Parallel()
	bad := ident.Principal{}
	checks := map[string]error{
		"CheckNew":         tickets.CheckNew(bad, urgentOn, &tickets.NewTicket{Title: "x"}),
		"CheckChange":      tickets.CheckChange(bad, urgentOn, ownTicket(open), &tickets.TicketChange{}),
		"CheckAssign":      tickets.CheckAssign(bad),
		"CheckComment":     tickets.CheckComment(bad, "hi"),
		"CheckBlockerEdit": tickets.CheckBlockerEdit(bad, ownTicket(open)),
	}
	for name, err := range checks {
		assert.ErrorIs(t, err, tickets.ErrInvalidPrincipal, name)
	}
}

func TestCheckNew(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		by   ident.Principal
		cfg  *tickets.Config
		nt   tickets.NewTicket
		want error
	}{
		"user minimal":                {user, urgentOff, tickets.NewTicket{Title: "x"}, nil},
		"agent minimal":               {agent1, urgentOff, tickets.NewTicket{Title: "x"}, nil},
		"empty title":                 {user, urgentOff, tickets.NewTicket{}, tickets.ErrInvalid},
		"blank title":                 {user, urgentOff, tickets.NewTicket{Title: " \t\n"}, tickets.ErrInvalid},
		"NUL in title":                {user, urgentOff, tickets.NewTicket{Title: "a\x00"}, tickets.ErrInvalid},
		"NUL in body":                 {user, urgentOff, tickets.NewTicket{Title: "a", Body: "b\x00"}, tickets.ErrInvalid},
		"NUL in urgent reason":        {user, urgentOff, tickets.NewTicket{Title: "a", Urgent: true, UrgentReason: "\x00"}, tickets.ErrInvalid},
		"status preparing":            {agent1, urgentOff, tickets.NewTicket{Title: "x", Status: preparing}, nil},
		"user files open":             {user, urgentOff, tickets.NewTicket{Title: "x", Status: open}, nil},
		"agent files open":            {agent1, urgentOff, tickets.NewTicket{Title: "x", Status: open}, tickets.ErrNotPermitted},
		"user files discussion":       {user, urgentOff, tickets.NewTicket{Title: "x", Status: discussion}, tickets.ErrInvalid},
		"user files in_progress":      {user, urgentOff, tickets.NewTicket{Title: "x", Status: inProgress}, tickets.ErrInvalid},
		"user files done":             {user, urgentOff, tickets.NewTicket{Title: "x", Status: done}, tickets.ErrInvalid},
		"user files unknown status":   {user, urgentOff, tickets.NewTicket{Title: "x", Status: "limbo"}, tickets.ErrInvalid},
		"user sets AutoMerge":         {user, urgentOff, tickets.NewTicket{Title: "x", AutoMerge: true}, nil},
		"agent sets AutoMerge":        {agent1, urgentOn, tickets.NewTicket{Title: "x", AutoMerge: true}, tickets.ErrAutoMergeUserOnly},
		"user urgent, cfg off":        {user, urgentOff, tickets.NewTicket{Title: "x", Urgent: true}, nil},
		"user urgent with reason":     {user, urgentOff, tickets.NewTicket{Title: "x", Urgent: true, UrgentReason: "r"}, nil},
		"agent urgent, cfg off":       {agent1, urgentOff, tickets.NewTicket{Title: "x", Urgent: true, UrgentReason: "r"}, tickets.ErrUrgentNotAllowed},
		"agent urgent, no reason":     {agent1, urgentOn, tickets.NewTicket{Title: "x", Urgent: true}, tickets.ErrUrgentReasonRequired},
		"agent urgent, blank reason":  {agent1, urgentOn, tickets.NewTicket{Title: "x", Urgent: true, UrgentReason: "  "}, tickets.ErrUrgentReasonRequired},
		"agent urgent with reason":    {agent1, urgentOn, tickets.NewTicket{Title: "x", Urgent: true, UrgentReason: "r"}, nil},
		"agent not urgent, cfg off":   {agent1, urgentOff, tickets.NewTicket{Title: "x"}, nil},
		"reason without urgent":       {user, urgentOn, tickets.NewTicket{Title: "x", UrgentReason: "r"}, tickets.ErrInvalid},
		"blank reason without urgent": {user, urgentOn, tickets.NewTicket{Title: "x", UrgentReason: " "}, nil},
		"repos":                       {user, urgentOff, tickets.NewTicket{Title: "x", Repos: []string{"a", "b"}}, nil},
		"blank repo":                  {user, urgentOff, tickets.NewTicket{Title: "x", Repos: []string{"a", " "}}, tickets.ErrInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertErr(t, c.want, tickets.CheckNew(c.by, c.cfg, &c.nt))
		})
	}
}

func TestErrorKinds(t *testing.T) {
	t.Parallel()
	err := tickets.CheckNew(agent1, urgentOff, &tickets.NewTicket{Title: "x", Status: open})
	require.ErrorIs(t, err, tickets.ErrNotPermitted)
	assert.NotErrorIs(t, err, tickets.ErrInvalid)

	err = tickets.CheckNew(user, urgentOff, &tickets.NewTicket{Title: "x", Status: done})
	require.ErrorIs(t, err, tickets.ErrInvalid)
	assert.NotErrorIs(t, err, tickets.ErrNotPermitted)
	assert.ErrorContains(t, err, `"done"`)

	assert.ErrorIs(t, tickets.ErrUrgentReasonRequired, tickets.ErrInvalid)
	assert.NotErrorIs(t, tickets.ErrUrgentReasonRequired, tickets.ErrNotPermitted)
	for _, e := range []error{
		tickets.ErrAutoMergeUserOnly, tickets.ErrUrgentNotAllowed, tickets.ErrUserOnly, tickets.ErrNotEditable,
		tickets.ErrAutoMergeScopeLocked, tickets.ErrStatusNotPermitted, tickets.ErrBlockersNotEditable,
		tickets.ErrBlockerNotRemovable,
	} {
		assert.ErrorIs(t, e, tickets.ErrNotPermitted, e)
	}
}

func TestInitialStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, preparing, tickets.InitialStatus(&tickets.NewTicket{}))
	assert.Equal(t, preparing, tickets.InitialStatus(&tickets.NewTicket{Status: preparing}))
	assert.Equal(t, open, tickets.InitialStatus(&tickets.NewTicket{Status: open}))
}

func TestInheritedAutoMerge(t *testing.T) {
	t.Parallel()
	for _, asked := range []bool{false, true} {
		for _, parent := range []bool{false, true} {
			n := &tickets.NewTicket{AutoMerge: asked}
			p := &tickets.Ticket{AutoMerge: parent}
			assert.Equal(t, asked, tickets.InheritedAutoMerge(user, n, p), "user: asked %v, parent %v", asked, parent)
			assert.Equal(t, parent, tickets.InheritedAutoMerge(agent1, n, p), "agent: asked %v, parent %v", asked, parent)
		}
	}
}

func TestInheritedRepos(t *testing.T) {
	t.Parallel()
	parent := &tickets.Ticket{Repos: []string{"p1", "p2"}}
	cases := map[string]struct {
		by     ident.Principal
		repos  []string
		parent *tickets.Ticket
		want   []string
	}{
		"user names none":             {user, nil, parent, nil},
		"user names some":             {user, []string{"u"}, parent, []string{"u"}},
		"agent names some":            {agent1, []string{"a"}, parent, []string{"a"}},
		"agent names none":            {agent1, nil, parent, []string{"p1", "p2"}},
		"agent names none, no parent": {agent1, nil, nil, nil},
		"agent, parent has none":      {agent1, nil, &tickets.Ticket{}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := tickets.InheritedRepos(c.by, &tickets.NewTicket{Repos: c.repos}, c.parent)
			assert.Equal(t, c.want, got)
		})
	}

	t.Run("a clone", func(t *testing.T) {
		t.Parallel()
		p := &tickets.Ticket{Repos: []string{"p1"}}
		got := tickets.InheritedRepos(agent1, &tickets.NewTicket{}, p)
		got[0] = "changed"
		assert.Equal(t, []string{"p1"}, p.Repos)
	})
}

func TestCheckChange(t *testing.T) {
	t.Parallel()
	blocked := func(status tickets.Status) *tickets.Ticket {
		tk := ownTicket(status)
		tk.BlockedBy = []tickets.TicketRef{{ID: "7"}, {ID: "8"}}
		return tk
	}
	autoMerged := filedBy1(open)
	autoMerged.AutoMerge = true
	ownAutoMerged := ownTicket(open)
	ownAutoMerged.AutoMerge = true
	agent2Filed := &tickets.Ticket{ID: "9", Status: open, CreatedBy: agent2}
	sameAgentOtherTicket := &tickets.Ticket{ID: "9", Status: open, CreatedBy: ident.AgentPrincipal("a1", "5")}
	urgentSet := &tickets.UrgentChange{Urgent: true, Reason: "r"}

	cases := map[string]struct {
		by     ident.Principal
		cfg    *tickets.Config
		cur    *tickets.Ticket
		change tickets.TicketChange
		want   error
	}{
		// IfStatus
		"IfStatus holds":         {user, urgentOff, ownTicket(open), tickets.TicketChange{IfStatus: ptr(open), Title: ptr("t")}, nil},
		"IfStatus differs":       {user, urgentOff, ownTicket(open), tickets.TicketChange{IfStatus: ptr(done), Title: ptr("t")}, tickets.ErrStatusChanged},
		"IfStatus checked first": {agent2, urgentOff, ownTicket(open), tickets.TicketChange{IfStatus: ptr(done), Title: ptr("t")}, tickets.ErrStatusChanged},

		// retried status calls
		"retry on own terminal ticket":      {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done)}, nil},
		"retry with IfStatus":               {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), IfStatus: ptr(done)}, nil},
		"retry with IfStatus mismatch":      {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), IfStatus: ptr(open)}, tickets.ErrStatusChanged},
		"retry plus a title, terminal":      {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), Title: ptr("t")}, tickets.ErrNotEditable},
		"retry plus a title, live":          {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(open), Title: ptr("t")}, nil},
		"retry plus a body, terminal":       {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), Body: ptr("b")}, tickets.ErrNotEditable},
		"retry plus agent, terminal":        {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), Agent: &tickets.AgentSpec{}}, tickets.ErrNotEditable},
		"retry plus AutoMerge":              {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(open), AutoMerge: ptr(false)}, tickets.ErrAutoMergeUserOnly},
		"retry plus urgent":                 {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(open), Urgent: &tickets.UrgentChange{}}, tickets.ErrUrgentNotAllowed},
		"retry plus repos, terminal":        {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(done), Repos: ptr([]string{})}, tickets.ErrNotEditable},
		"empty change by user":              {user, urgentOff, ownTicket(open), tickets.TicketChange{}, nil},
		"empty change on terminal by agent": {agent1, urgentOff, ownTicket(done), tickets.TicketChange{}, tickets.ErrNotEditable},

		// user
		"user moves status":                  {user, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(inProgress)}, nil},
		"user reopens":                       {user, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(open)}, nil},
		"user leaves discussion":             {user, urgentOff, ownTicket(discussion), tickets.TicketChange{Status: ptr(open)}, nil},
		"user edits terminal ticket":         {user, urgentOff, ownTicket(done), tickets.TicketChange{Title: ptr("t")}, nil},
		"user edits another's ticket":        {user, urgentOff, agent2Filed, tickets.TicketChange{Title: ptr("t")}, nil},
		"user sets AutoMerge":                {user, urgentOff, filedBy1(open), tickets.TicketChange{AutoMerge: ptr(true)}, nil},
		"user rewrites AutoMerge ticket":     {user, urgentOff, autoMerged, tickets.TicketChange{Title: ptr("t"), Body: ptr("b")}, nil},
		"user invalid transition":            {user, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrInvalidTransition},
		"user unknown status":                {user, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(tickets.Status("limbo"))}, tickets.ErrInvalid},
		"user blank title":                   {user, urgentOff, ownTicket(open), tickets.TicketChange{Title: ptr("  ")}, tickets.ErrInvalid},
		"user empty title":                   {user, urgentOff, ownTicket(open), tickets.TicketChange{Title: ptr("")}, tickets.ErrInvalid},
		"user empty body is fine":            {user, urgentOff, ownTicket(open), tickets.TicketChange{Body: ptr("")}, nil},
		"user NUL title":                     {user, urgentOff, ownTicket(open), tickets.TicketChange{Title: ptr("a\x00")}, tickets.ErrInvalid},
		"user NUL body":                      {user, urgentOff, ownTicket(open), tickets.TicketChange{Body: ptr("a\x00")}, tickets.ErrInvalid},
		"user NUL urgent reason":             {user, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true, Reason: "\x00"}}, tickets.ErrInvalid},
		"user blank repo":                    {user, urgentOff, ownTicket(open), tickets.TicketChange{Repos: ptr([]string{"a", ""})}, tickets.ErrInvalid},
		"user clears repos":                  {user, urgentOff, ownTicket(open), tickets.TicketChange{Repos: ptr([]string{})}, nil},
		"user repos":                         {user, urgentOff, ownTicket(open), tickets.TicketChange{Repos: ptr([]string{"a"})}, nil},
		"user urgent, cfg off":               {user, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true}}, nil},
		"user urgent with reason":            {user, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: urgentSet}, nil},
		"user clears urgent":                 {user, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{}}, nil},
		"user reason without urgent":         {user, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Reason: "r"}}, tickets.ErrInvalid},
		"user into in_progress, blocked":     {user, urgentOff, blocked(open), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrBlocked},
		"user into in_progress, free":        {user, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(inProgress)}, nil},
		"discussion to in_progress, blocked": {user, urgentOff, blocked(discussion), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrBlocked},
		"in_progress edit, blocked":          {user, urgentOff, blocked(inProgress), tickets.TicketChange{Status: ptr(inProgress), Title: ptr("t")}, nil},
		"blocked ticket to done":             {user, urgentOff, blocked(inProgress), tickets.TicketChange{Status: ptr(done)}, nil},
		"blocked ticket to discussion":       {user, urgentOff, blocked(open), tickets.TicketChange{Status: ptr(discussion)}, nil},

		// agent on its own ticket
		"agent edits own ticket":         {agent1, urgentOff, ownTicket(inProgress), tickets.TicketChange{Title: ptr("t"), Body: ptr("b")}, nil},
		"agent edits own terminal":       {agent1, urgentOff, ownTicket(cancelled), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent moves own status":         {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(inProgress)}, nil},
		"agent finishes own":             {agent1, urgentOff, ownTicket(inProgress), tickets.TicketChange{Status: ptr(done)}, nil},
		"agent into in_progress blocked": {agent1, urgentOff, blocked(open), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrBlocked},
		"agent reopens own done":         {agent1, urgentOff, ownTicket(done), tickets.TicketChange{Status: ptr(open)}, tickets.ErrUserOnly},
		"agent reopens own cancelled":    {agent1, urgentOff, ownTicket(cancelled), tickets.TicketChange{Status: ptr(preparing)}, tickets.ErrUserOnly},
		"agent leaves discussion":        {agent1, urgentOff, ownTicket(discussion), tickets.TicketChange{Status: ptr(open)}, tickets.ErrUserOnly},
		"agent walks discussion on":      {agent1, urgentOff, ownTicket(discussion), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrUserOnly},
		"agent cancels from discussion":  {agent1, urgentOff, ownTicket(discussion), tickets.TicketChange{Status: ptr(cancelled)}, tickets.ErrUserOnly},
		"agent enters discussion":        {agent1, urgentOff, ownTicket(inProgress), tickets.TicketChange{Status: ptr(discussion)}, nil},
		"agent invalid transition":       {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(done)}, tickets.ErrInvalidTransition},
		"agent unknown status":           {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Status: ptr(tickets.Status("limbo"))}, tickets.ErrInvalid},
		"agent sets AutoMerge on own":    {agent1, urgentOff, ownTicket(open), tickets.TicketChange{AutoMerge: ptr(true)}, tickets.ErrAutoMergeUserOnly},
		"agent clears AutoMerge on own":  {agent1, urgentOff, ownAutoMerged, tickets.TicketChange{AutoMerge: ptr(false)}, tickets.ErrAutoMergeUserOnly},
		"agent edits own AutoMerge":      {agent1, urgentOff, ownAutoMerged, tickets.TicketChange{Title: ptr("t"), Body: ptr("b"), Repos: ptr([]string{"r"}), Agent: &tickets.AgentSpec{}}, nil},
		"agent blank title":              {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Title: ptr(" ")}, tickets.ErrInvalid},
		"agent NUL body":                 {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Body: ptr("\x00")}, tickets.ErrInvalid},
		"agent blank repo":               {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Repos: ptr([]string{" "})}, tickets.ErrInvalid},

		// agent on tickets it filed, or others'
		"agent edits filed preparing":         {agent1, urgentOff, filedBy1(preparing), tickets.TicketChange{Title: ptr("t")}, nil},
		"agent edits filed open":              {agent1, urgentOff, filedBy1(open), tickets.TicketChange{Title: ptr("t")}, nil},
		"agent edits filed discussion":        {agent1, urgentOff, filedBy1(discussion), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits filed in_progress":       {agent1, urgentOff, filedBy1(inProgress), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits filed done":              {agent1, urgentOff, filedBy1(done), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits filed misconfigured":     {agent1, urgentOff, filedBy1(misconfigured), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits user's ticket":           {agent1, urgentOff, usersTicket(open), tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits another agent's":         {agent1, urgentOff, agent2Filed, tickets.TicketChange{Title: ptr("t")}, tickets.ErrNotEditable},
		"agent edits ticket filed by its id":  {agent1, urgentOff, sameAgentOtherTicket, tickets.TicketChange{Title: ptr("t")}, nil},
		"agent cancels filed open":            {agent1, urgentOff, filedBy1(open), tickets.TicketChange{Status: ptr(cancelled)}, nil},
		"agent cancels filed preparing":       {agent1, urgentOff, filedBy1(preparing), tickets.TicketChange{Status: ptr(cancelled)}, nil},
		"agent opens filed preparing":         {agent1, urgentOff, filedBy1(preparing), tickets.TicketChange{Status: ptr(open)}, tickets.ErrStatusNotPermitted},
		"agent starts filed open":             {agent1, urgentOff, filedBy1(open), tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrStatusNotPermitted},
		"agent cancels filed in_progress":     {agent1, urgentOff, filedBy1(inProgress), tickets.TicketChange{Status: ptr(cancelled)}, tickets.ErrNotEditable},
		"agent reopens filed done":            {agent1, urgentOff, filedBy1(done), tickets.TicketChange{Status: ptr(open)}, tickets.ErrUserOnly},
		"agent takes filed out of discussion": {agent1, urgentOff, filedBy1(discussion), tickets.TicketChange{Status: ptr(open)}, tickets.ErrUserOnly},
		"agent cancels another's ticket":      {agent1, urgentOff, agent2Filed, tickets.TicketChange{Status: ptr(cancelled)}, tickets.ErrNotEditable},
		"agent starts another's ticket":       {agent1, urgentOff, agent2Filed, tickets.TicketChange{Status: ptr(inProgress)}, tickets.ErrStatusNotPermitted},
		"agent sets AutoMerge on filed":       {agent1, urgentOff, filedBy1(open), tickets.TicketChange{AutoMerge: ptr(true)}, tickets.ErrAutoMergeUserOnly},

		// AutoMerge scope lock
		"agent retitles filed AutoMerge":      {agent1, urgentOff, autoMerged, tickets.TicketChange{Title: ptr("t")}, tickets.ErrAutoMergeScopeLocked},
		"agent rebodies filed AutoMerge":      {agent1, urgentOff, autoMerged, tickets.TicketChange{Body: ptr("b")}, tickets.ErrAutoMergeScopeLocked},
		"agent repos filed AutoMerge":         {agent1, urgentOff, autoMerged, tickets.TicketChange{Repos: ptr([]string{})}, tickets.ErrAutoMergeScopeLocked},
		"agent respecs filed AutoMerge":       {agent1, urgentOff, autoMerged, tickets.TicketChange{Agent: &tickets.AgentSpec{}}, tickets.ErrAutoMergeScopeLocked},
		"agent cancels filed AutoMerge":       {agent1, urgentOff, autoMerged, tickets.TicketChange{Status: ptr(cancelled)}, nil},
		"agent urgent on filed AutoMerge":     {agent1, urgentOn, autoMerged, tickets.TicketChange{Urgent: urgentSet}, nil},
		"scope lock beats the AutoMerge flag": {agent1, urgentOff, autoMerged, tickets.TicketChange{Title: ptr("t"), AutoMerge: ptr(true)}, tickets.ErrAutoMergeScopeLocked},
		"agent edits filed without AutoMerge": {agent1, urgentOff, filedBy1(open), tickets.TicketChange{Title: ptr("t"), Body: ptr("b"), Repos: ptr([]string{}), Agent: &tickets.AgentSpec{}}, nil},

		// urgent
		"agent urgent, cfg off":        {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: urgentSet}, tickets.ErrUrgentNotAllowed},
		"agent clears urgent, cfg off": {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{}}, tickets.ErrUrgentNotAllowed},
		"agent urgent, no reason":      {agent1, urgentOn, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true}}, tickets.ErrUrgentReasonRequired},
		"agent urgent, blank reason":   {agent1, urgentOn, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true, Reason: " "}}, tickets.ErrUrgentReasonRequired},
		"agent urgent with reason":     {agent1, urgentOn, ownTicket(open), tickets.TicketChange{Urgent: urgentSet}, nil},
		"agent clears urgent":          {agent1, urgentOn, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{}}, nil},
		"agent reason without urgent":  {agent1, urgentOn, ownTicket(open), tickets.TicketChange{Urgent: &tickets.UrgentChange{Reason: "r"}}, tickets.ErrInvalid},
		"agent urgent on uneditable":   {agent1, urgentOn, usersTicket(open), tickets.TicketChange{Urgent: urgentSet}, tickets.ErrNotEditable},
		"agent leaves urgent alone":    {agent1, urgentOff, ownTicket(open), tickets.TicketChange{Title: ptr("t")}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertErr(t, c.want, tickets.CheckChange(c.by, c.cfg, c.cur, &c.change))
		})
	}
}

func TestCheckChangeBlockedMessageCountsBlockers(t *testing.T) {
	t.Parallel()
	cur := ownTicket(open)
	cur.BlockedBy = []tickets.TicketRef{{ID: "7"}, {ID: "8"}}
	err := tickets.CheckChange(user, urgentOff, cur, &tickets.TicketChange{Status: ptr(inProgress)})
	require.ErrorIs(t, err, tickets.ErrBlocked)
	assert.ErrorContains(t, err, "waits on 2 open blocker(s)")
}

func TestCheckAssign(t *testing.T) {
	t.Parallel()
	assert.NoError(t, tickets.CheckAssign(user))
	assert.ErrorIs(t, tickets.CheckAssign(agent1), tickets.ErrUserOnly)
}

func TestCheckComment(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		by   ident.Principal
		body string
		want error
	}{
		"user":      {user, "hi", nil},
		"agent":     {agent1, "hi", nil},
		"empty":     {user, "", tickets.ErrInvalid},
		"blank":     {agent1, " \n\t", tickets.ErrInvalid},
		"NUL":       {user, "a\x00b", tickets.ErrInvalid},
		"bad by":    {ident.Principal{}, "hi", tickets.ErrInvalidPrincipal},
		"bad+blank": {ident.Principal{}, "", tickets.ErrInvalidPrincipal},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertErr(t, c.want, tickets.CheckComment(c.by, c.body))
		})
	}
}

func TestUrgentReason(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "why", tickets.UrgentReason(true, "  why \n"))
	assert.Empty(t, tickets.UrgentReason(true, "  "))
	assert.Empty(t, tickets.UrgentReason(false, "why"))
}

func TestAgentMayEdit(t *testing.T) {
	t.Parallel()
	for _, s := range tickets.Statuses() {
		assert.Equal(t, !s.Terminal(), tickets.AgentMayEdit(agent1, ownTicket(s)), "own, %s", s)
		assert.Equal(t, s == preparing || s == open, tickets.AgentMayEdit(agent1, filedBy1(s)), "filed, %s", s)
		assert.False(t, tickets.AgentMayEdit(agent1, usersTicket(s)), "user's, %s", s)
		other := &tickets.Ticket{ID: "9", Status: s, CreatedBy: agent2}
		assert.False(t, tickets.AgentMayEdit(agent1, other), "another agent's, %s", s)
	}
}

func TestCheckBlockerEdit(t *testing.T) {
	t.Parallel()
	for _, s := range tickets.Statuses() {
		assert.NoError(t, tickets.CheckBlockerEdit(user, usersTicket(s)), "user, %s", s)
	}
	assert.NoError(t, tickets.CheckBlockerEdit(agent1, ownTicket(open)))
	assert.NoError(t, tickets.CheckBlockerEdit(agent1, filedBy1(preparing)))
	assert.ErrorIs(t, tickets.CheckBlockerEdit(agent1, ownTicket(done)), tickets.ErrBlockersNotEditable)
	assert.ErrorIs(t, tickets.CheckBlockerEdit(agent1, filedBy1(inProgress)), tickets.ErrBlockersNotEditable)
	assert.ErrorIs(t, tickets.CheckBlockerEdit(agent1, usersTicket(open)), tickets.ErrBlockersNotEditable)
	assert.ErrorIs(t, tickets.CheckBlockerEdit(agent2, filedBy1(open)), tickets.ErrBlockersNotEditable)
}

func TestCheckBlockerRemoval(t *testing.T) {
	t.Parallel()
	assert.NoError(t, tickets.CheckBlockerRemoval(user, user))
	assert.NoError(t, tickets.CheckBlockerRemoval(user, agent1))
	assert.NoError(t, tickets.CheckBlockerRemoval(agent1, agent1))
	assert.NoError(t, tickets.CheckBlockerRemoval(agent1, ident.AgentPrincipal("a1", "5")), "the agent id decides, not its ticket")
	assert.ErrorIs(t, tickets.CheckBlockerRemoval(agent1, user), tickets.ErrBlockerNotRemovable)
	assert.ErrorIs(t, tickets.CheckBlockerRemoval(agent1, agent2), tickets.ErrBlockerNotRemovable)
}

func TestAgentCreated(t *testing.T) {
	t.Parallel()
	assert.True(t, filedBy1(open).AgentCreated())
	assert.False(t, usersTicket(open).AgentCreated())
	assert.False(t, (&tickets.Ticket{}).AgentCreated())
}
