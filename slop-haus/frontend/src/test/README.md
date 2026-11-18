# Frontend test layers

Which layer owns which assertion (the frontend part of the project's testing):

| Layer | Owns | Where |
| --- | --- | --- |
| Wire shape | Method, path, query, JSON body, what the client makes of the answer, CSRF and 401 handling per operation | `api/*_operations.test.ts` (the table in `api/operations_harness.ts`), `api/*_api.test.ts` |
| Client stack | Tenant header, CSRF, 401 refresh and retry, error envelope, run through real `fetch` against a mock API | `app_tests/real_client_stack.test.ts` (MSW, `test/msw_server.ts`) |
| Behaviour | What a component or page does for a given answer; mocks at the method level (`vi.spyOn(apiClient)`) | `*.test.tsx`, `app_tests/` |
| Visual and a11y | Look, states and accessibility | Storybook stories and their browser tests |

## Test doubles are checked against the API

A double that only the compiler checks can still drift from the server, so
three things hold bodies to `api_generated/openapi.json` (through
`test/openapi_schema.ts`, an ajv wrapper):

- `test/fixtures.test.ts` validates every `make*` factory against its schema.
  A new factory needs a row there.
- The operations harness validates each row's `body` and `response` against
  the operation's request and success response schemas.
- `serve()` in `test/msw_server.ts` refuses to register a handler whose body
  breaks the spec.

A failure names the spec path (`.../TicketBody/created_at: must be string`).
When the server's schema changes, fix the fixture; component tests built from
it then follow.

The exception is `enum`s of nullable types: the spec lists the values without
`null`, so `openapi_schema.ts` admits it.
