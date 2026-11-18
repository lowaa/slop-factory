package wireschema_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/wireschema"
)

type color string

func (color) Schema(r huma.Registry) *huma.Schema {
	return wireschema.Enum(r, "Color", []color{"red", "green"})
}

type leaf struct {
	Tags []string `json:"tags"`
}

// note is in both a request and a response.
type note struct {
	Lines []string `json:"lines"`
}

type Base struct {
	Inherited []int `json:"inherited"`
}

type widgetBody struct {
	Base
	Color    color             `json:"color"`
	Leaf     *leaf             `json:"leaf"`
	Note     *note             `json:"note"`
	Optional *leaf             `json:"optional,omitempty"`
	Owner    *uuid.UUID        `json:"owner"`
	Items    []leaf            `json:"items"`
	Grid     [][]int           `json:"grid"`
	Labels   map[string]string `json:"labels"`
	Groups   map[string][]int  `json:"groups"`
	Versions []*string         `json:"versions"`
	Leaves   []*leaf           `json:"leaves"`
	Diff     []string          `json:"diff" nullable:"true"`
	Omitted  []string          `json:"omitted,omitempty"`
	Raw      []byte            `json:"raw"`
	At       time.Time         `json:"at"`
}

type widgetInput struct {
	Color color    `json:"color"`
	Tags  []string `json:"tags"`
	Note  *note    `json:"note"`
}

type widgetRequest struct {
	Body widgetInput
}

type widgetResponse struct {
	Body widgetBody
}

// newAPI serves one operation answering with body, configured the way
// apiserver configures the real API.
func newAPI(t *testing.T, body widgetBody) humatest.TestAPI {
	t.Helper()
	config := huma.DefaultConfig("test", "1.0.0")
	config.CreateHooks = nil
	config.Transformers = append(config.Transformers, wireschema.NonNilCollections)
	_, api := humatest.New(t, config)
	huma.Register(api, huma.Operation{OperationID: "putWidget", Method: http.MethodPut, Path: "/widget"},
		func(context.Context, *widgetRequest) (*widgetResponse, error) {
			return &widgetResponse{Body: body}, nil
		})
	wireschema.ExactResponses(api.OpenAPI())
	return api
}

// schemaJSON is a component schema as the generated spec spells it.
func schemaJSON(t *testing.T, api huma.API, name string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(api.OpenAPI().Components.Schemas.Map()[name])
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func property(t *testing.T, schema map[string]any, name string) any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "no properties in %v", schema)
	require.Contains(t, props, name)
	return props[name]
}

func TestExactResponses_MarksNullWhereTheServerSendsIt(t *testing.T) {
	api := newAPI(t, widgetBody{})
	body := schemaJSON(t, api, "WidgetBody")

	for name, want := range map[string]string{
		"leaf": `{"anyOf":[{"$ref":"#/components/schemas/Leaf"},{"type":"null"}]}`,
		"note": `{"anyOf":[{"$ref":"#/components/schemas/Note"},{"type":"null"}]}`,
		// huma decays a pointer to an array type, as uuid.UUID is, to the
		// array's own non-null schema.
		"owner":     `{"type":["string","null"]}`,
		"optional":  `{"$ref":"#/components/schemas/Leaf"}`,
		"items":     `{"type":"array","items":{"$ref":"#/components/schemas/Leaf"}}`,
		"grid":      `{"type":"array","items":{"type":"array","items":{"type":"integer","format":"int64"}}}`,
		"groups":    `{"type":"object","additionalProperties":{"type":"array","items":{"type":"integer","format":"int64"}}}`,
		"diff":      `{"type":["array","null"],"items":{"type":"string"}}`,
		"versions":  `{"type":"array","items":{"type":["string","null"]}}`,
		"leaves":    `{"type":"array","items":{"anyOf":[{"$ref":"#/components/schemas/Leaf"},{"type":"null"}]}}`,
		"omitted":   `{"type":"array","items":{"type":"string"}}`,
		"inherited": `{"type":"array","items":{"type":"integer","format":"int64"}}`,
		"color":     `{"$ref":"#/components/schemas/Color"}`,
	} {
		got, err := json.Marshal(property(t, body, name))
		require.NoError(t, err)
		assert.JSONEq(t, want, string(got), name)
	}

	leafSchema := schemaJSON(t, api, "Leaf")
	got, err := json.Marshal(property(t, leafSchema, "tags"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"array","items":{"type":"string"}}`, string(got), "a nested response schema too")
}

// huma validates requests against these same schemas: a request's are
// left exactly as huma made them.
func TestExactResponses_LeavesRequestSchemasAlone(t *testing.T) {
	api := newAPI(t, widgetBody{})
	input := schemaJSON(t, api, "WidgetInput")

	tags, err := json.Marshal(property(t, input, "tags"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":["array","null"],"items":{"type":"string"}}`, string(tags))
	noteRef, err := json.Marshal(property(t, input, "note"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"$ref":"#/components/schemas/Note"}`, string(noteRef))
	lines, err := json.Marshal(property(t, schemaJSON(t, api, "Note"), "lines"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":["array","null"],"items":{"type":"string"}}`, string(lines),
		"a schema a response shares with a request is the request's too")

	resp := api.Put("/widget", map[string]any{"color": "red", "tags": nil, "note": map[string]any{"lines": nil}})
	assert.Equal(t, http.StatusOK, resp.Code, "a null request array is still accepted: %s", resp.Body.String())
}

func TestEnum_IsOneSharedSchemaThatRequestsAreValidatedAgainst(t *testing.T) {
	api := newAPI(t, widgetBody{})

	color := schemaJSON(t, api, "Color")
	assert.Equal(t, map[string]any{"type": "string", "enum": []any{"red", "green"}}, color)

	resp := api.Put("/widget", map[string]any{"color": "blue", "tags": []string{}, "note": map[string]any{"lines": []string{}}})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
	assert.Contains(t, resp.Body.String(), "body.color")

	resp = api.Put("/widget", map[string]any{"color": "green", "tags": []string{}, "note": map[string]any{"lines": []string{}}})
	assert.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
}

func TestNonNilCollections_SendsNilCollectionsAsEmpty(t *testing.T) {
	shared := &leaf{}
	items := []leaf{{}, {Tags: []string{"kept"}}}
	grid := [][]int{nil, {1}}
	groups := map[string][]int{"none": nil, "some": {2}}
	handlerBody := widgetBody{
		Leaf:   shared,
		Items:  items,
		Grid:   grid,
		Groups: groups,
		At:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	api := newAPI(t, handlerBody)

	resp := api.Put("/widget", map[string]any{"color": "red", "tags": []string{}, "note": map[string]any{"lines": []string{}}})
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.JSONEq(t, `{
		"inherited": [],
		"color": "",
		"leaf": {"tags": []},
		"note": null,
		"owner": null,
		"items": [{"tags": []}, {"tags": ["kept"]}],
		"grid": [[], [1]],
		"labels": {},
		"groups": {"none": [], "some": [2]},
		"versions": [],
		"leaves": [],
		"diff": null,
		"raw": null,
		"at": "2026-01-02T03:04:05Z"
	}`, resp.Body.String())

	// What the handler returned may be shared -- a package-level value, say
	// -- so filling it in must not write to it.
	assert.Nil(t, shared.Tags)
	assert.Nil(t, items[0].Tags)
	assert.Nil(t, grid[0])
	assert.Nil(t, groups["none"])
}

func TestNonNilCollections_ReturnsAnUnchangedBodyAsItIs(t *testing.T) {
	body := &leaf{Tags: []string{"a"}}
	got, err := wireschema.NonNilCollections(nil, "200", body)
	require.NoError(t, err)
	assert.Same(t, body, got)

	got, err = wireschema.NonNilCollections(nil, "200", nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}
