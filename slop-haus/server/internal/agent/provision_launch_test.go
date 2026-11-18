package agent_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rcEntry(name, launch string) string {
	if launch == "" {
		return fmt.Sprintf(`{"kind":"remote_control","name":%q,"state":"running","resume_generation":0}`, name)
	}
	return fmt.Sprintf(`{"kind":"remote_control","name":%q,"state":"running","resume_generation":0,"launch":%q}`, name, launch)
}

// echoLaunch is the document as read, entries sent back with the launch they
// have, the way a client that knows the field does.
func echoLaunch(doc desiredStateJSON, change func(*desiredAgentJSON)) string {
	out := ""
	for i := range doc.Agents {
		a := doc.Agents[i]
		if change != nil {
			change(&a)
		}
		if out != "" {
			out += ","
		}
		out += fmt.Sprintf(`{"id":%q,"kind":%q,"name":%q,"harness":%q,"state":%q,"resume_generation":%d,"launch":%q}`,
			a.ID, a.Kind, a.Name, a.Harness, a.State, a.ResumeGeneration, a.Launch)
	}
	return out
}

func TestANewRemoteControlAgentIsLaunchedBySupervisorUnlessSaidOtherwise(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc-default", "")+","+rcEntry("rc-manual", "manual")+","+rcEntry("rc-super", "supervisor"))

	launch := map[string]string{}
	for _, a := range doc.Agents {
		launch[a.Name] = a.Launch
	}
	assert.Equal(t, map[string]string{"rc-default": "supervisor", "rc-manual": "manual", "rc-super": "supervisor"}, launch)

	for _, a := range p.read(t).Agents {
		assert.Equal(t, launch[a.Name], a.Launch, "the supervisor reads the same")
	}
}

func TestLaunchRoundTripsAndChangingItMovesTheGeneration(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc", "manual"))
	require.Equal(t, "manual", doc.Agents[0].Launch)
	gen := doc.Agents[0].Generation

	same := p.mustPut(t, doc.Revision, echoLaunch(doc, nil))
	assert.Equal(t, gen, same.Agents[0].Generation, "an unchanged launch is not a change")

	flipped := p.mustPut(t, same.Revision, echoLaunch(same, func(a *desiredAgentJSON) { a.Launch = "supervisor" }))
	assert.Equal(t, "supervisor", flipped.Agents[0].Launch)
	assert.Equal(t, gen+1, flipped.Agents[0].Generation)
}

func TestAWriteThatOmitsLaunchKeepsTheEntrysLaunch(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc", "manual"))

	// echo is the old client's write: no launch field.
	after := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "paused" }))
	assert.Equal(t, "manual", after.Agents[0].Launch)
	assert.Equal(t, "paused", after.Agents[0].State)
}

func TestOtherKindsHaveNoLaunch(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	assert.Empty(t, doc.Agents[0].Launch)
	assert.Empty(t, p.read(t).Agents[0].Launch)

	code, _ := p.put(doc.Revision, echo(doc, nil)+`,{"kind":"slot","name":"slot-2","state":"running","resume_generation":0,"launch":"supervisor"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code, "launch is for remote_control entries")
}

func TestALaunchThatIsNeitherValueIsRefused(t *testing.T) {
	p := newProvisioning(t)
	code, _ := p.put(0, rcEntry("rc", "robot"))
	assert.Equal(t, http.StatusUnprocessableEntity, code)
}

func TestARowWrittenWithoutALaunchReadsAsManual(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	_, err := p.database.Exec(p.ctx, `
		INSERT INTO supervisor_desired_agents (id, supervisor_id, kind, name, harness, state, generation)
		VALUES (gen_random_uuid(), $1, 'remote_control', 'rc-old', 'claude', 'running', 1)`, p.sup.ID)
	require.NoError(t, err)

	for _, a := range p.read(t).Agents {
		if a.Name == "rc-old" {
			assert.Equal(t, "manual", a.Launch, "an entry that predates the field, or is imported, keeps today's behaviour")
			return
		}
	}
	t.Fatalf("rc-old not read back; document %+v", doc)
}
