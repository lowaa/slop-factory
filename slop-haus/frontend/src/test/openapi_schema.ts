// Checks a value against the API's published contract (api_generated/openapi.json),
// so a test double that drifts from the server fails by naming the schema
// path it broke. Types alone guard only what the compiler sees; a recorded
// body typed `unknown`, or a fixture cast, slips past them.
import Ajv2020, { type ValidateFunction } from "ajv/dist/2020";
import addFormats from "ajv-formats";
import spec from "@/api_generated/openapi.json";

const SPEC_ID = "https://slop-haus.test/openapi.json";

interface Operation {
  requestBody?: { content?: Record<string, { schema?: unknown }> };
  responses?: Record<string, { content?: Record<string, { schema?: unknown }> }>;
}

// The server's generator lists a nullable enum's values without null (colour
// is `type: [string, null]`, `enum: [red, ...]`), though it sends null. The
// type is the intent, so the enum is widened to match rather than every
// fixture of a null colour failing.
function admitNullInEnums(node: unknown): unknown {
  if (Array.isArray(node)) return node.map(admitNullInEnums);
  if (node === null || typeof node !== "object") return node;
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(node)) out[key] = admitNullInEnums(value);
  const { type, enum: values } = out;
  if (Array.isArray(type) && type.includes("null") && Array.isArray(values) && !values.includes(null)) {
    out.enum = [...values, null];
  }
  return out;
}


export class OpenApiSchemas {
  private readonly ajv: Ajv2020;
  private readonly compiled = new Map<string, ValidateFunction>();
  private readonly templates: { template: string; pattern: RegExp }[];

  private readonly doc: typeof spec;

  constructor(doc: typeof spec = spec) {
    this.doc = doc;
    // OpenAPI's own keywords (discriminator, example) are annotations here.
    this.ajv = new Ajv2020({ strict: false, allErrors: true });
    addFormats(this.ajv);
    this.ajv.addSchema({ $id: SPEC_ID, ...(admitNullInEnums(doc) as object) });
    this.templates = Object.keys(doc.paths).map((template) => ({
      template,
      pattern: new RegExp(`^${template.replace(/\{[^}]+\}/g, "[^/]+")}$`),
    }));
  }

  // errors returns what is wrong with value against the named component
  // schema, one line per violation; empty when it conforms.
  errors(schemaName: string, value: unknown): string[] {
    if (!(schemaName in this.doc.components.schemas)) throw new Error(`no schema ${schemaName} in openapi.json`);
    return this.run(`#/components/schemas/${schemaName}`, { $ref: `${SPEC_ID}#/components/schemas/${schemaName}` }, value);
  }

  // requestErrors and responseErrors check a body against the operation that
  // method + url (path with its query, as sent) selects. An operation or
  // status the spec does not describe is itself an error.
  requestErrors(method: string, url: string, body: unknown): string[] {
    const { template, op } = this.operation(method, url);
    const schema = op.requestBody?.content?.["application/json"]?.schema;
    if (!schema) return [`${method} ${template}: the spec describes no JSON request body`];
    return this.run(`${method} ${template} request`, this.within(template, method, "requestBody/content/application~1json/schema", schema), body);
  }

  // successErrors checks a body against the operation's success response
  // (the lowest 2xx the spec declares).
  successErrors(method: string, url: string, body: unknown): string[] {
    const { op } = this.operation(method, url);
    const status = Object.keys(op.responses ?? {}).filter((s) => /^2\d\d$/.test(s)).sort()[0];
    if (!status) return [`${method} ${url}: the spec declares no 2xx response`];
    return this.responseErrors(method, url, Number(status), body);
  }

  responseErrors(method: string, url: string, status: number, body: unknown): string[] {
    const { template, op } = this.operation(method, url);
    const response = op.responses?.[String(status)] ?? op.responses?.default;
    const where = op.responses?.[String(status)] ? String(status) : "default";
    const schema = response?.content?.["application/json"]?.schema;
    if (!schema) return [`${method} ${template}: the spec describes no JSON response for ${status}`];
    return this.run(
      `${method} ${template} response ${status}`,
      this.within(template, method, `responses/${where}/content/application~1json/schema`, schema),
      body,
    );
  }

  private operation(method: string, url: string): { template: string; op: Operation } {
    const path = url.split("?")[0];
    const found = this.templates.find((t) => t.pattern.test(path) && (this.doc.paths as Record<string, Record<string, unknown>>)[t.template][method.toLowerCase()]);
    if (!found) throw new Error(`openapi.json has no ${method} ${path}`);
    const op = (this.doc.paths as Record<string, Record<string, Operation>>)[found.template][method.toLowerCase()];
    return { template: found.template, op };
  }

  // within refers to an inline schema by its position in the document, so
  // errors name a path a reader can find in openapi.json.
  private within(template: string, method: string, pointer: string, schema: unknown): unknown {
    const ref = (schema as { $ref?: string }).$ref;
    if (ref) return { $ref: `${SPEC_ID}${ref}` };
    return { $ref: `${SPEC_ID}#/paths/${template.replace(/~/g, "~0").replace(/\//g, "~1")}/${method.toLowerCase()}/${pointer}` };
  }

  private run(key: string, schema: unknown, value: unknown): string[] {
    let validate = this.compiled.get(key);
    if (!validate) {
      validate = this.ajv.compile(schema as object);
      this.compiled.set(key, validate);
    }
    if (validate(value)) return [];
    return (validate.errors ?? []).map((e) => `${key}${e.instancePath || "/"}: ${e.message}${e.params && "additionalProperty" in e.params ? ` (${String(e.params.additionalProperty)})` : ""}`);
  }
}

export const openApiSchemas = new OpenApiSchemas();
