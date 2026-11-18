// A stand-in for GitHub (API and website) for the e2e stack, run as the compose service
// `fake-github` (see ../../docker-compose-dev.e2e.yaml): the api is given its
// address as GITHUB_API_BASE_URL and GITHUB_WEB_BASE_URL. It decides nothing -- each route answers
// what the suite last scripted for it, and every request is recorded for the
// suite to read back. The one state it keeps is the branches that exist (see
// `branches` below).
import http from "node:http";

const PORT = 8083;
let script = {};
let requests = [];
let lastManifest = {};

function reply(res, status, body) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function readBody(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8");
}

// webPage is GitHub's website as far as the manifest flow goes. Like the API
// routes it decides nothing: the manifest page sends the browser back to the
// manifest's own redirect_url with the scripted code and the state it was
// given, and the install page to its setup_url with the scripted outcome.
function webPage(req, url, body) {
  const redirect = (to) => (res) => {
    res.writeHead(303, { Location: to });
    res.end();
  };
  if (req.method === "POST" && /^(\/organizations\/[^/]+)?\/settings\/apps\/new$/.test(url.pathname)) {
    lastManifest = JSON.parse(new URLSearchParams(body).get("manifest") ?? "{}");
    const back = new URL(lastManifest.redirect_url);
    back.searchParams.set("code", script.manifestCode?.body ?? "e2e-manifest-code-0123456789");
    back.searchParams.set("state", url.searchParams.get("state") ?? "");
    return redirect(back.toString());
  }
  if (req.method === "GET" && /^\/apps\/[^/]+\/installations\/new$/.test(url.pathname)) {
    const back = new URL(lastManifest.setup_url);
    const outcome = script.installPage?.body ?? {};
    back.searchParams.set("setup_action", outcome.setup_action ?? "install");
    if (outcome.installation_id) back.searchParams.set("installation_id", String(outcome.installation_id));
    back.searchParams.set("state", url.searchParams.get("state") ?? "");
    return redirect(back.toString());
  }
  return null;
}

// The one thing the fake remembers beyond the script: which branches exist and
// where they point, seeded from the script's `branches` ({name: sha}) and
// changed only by creating a ref. Creating one is how the server makes an
// attempt's branch, and the server tells a retry from a branch somebody put
// elsewhere by reading it back, so the fake must answer the same way twice.
let branches = new Map();

// gitRoute answers the routes that read or change `branches`, or null for any
// other request.
function gitRoute(req, url, body) {
  const repo = "/repos/[^/]+/[^/]+";
  if (req.method === "POST" && new RegExp(`^${repo}/git/refs$`).test(url.pathname)) {
    const { ref, sha } = JSON.parse(body || "{}");
    const name = String(ref ?? "").replace(/^refs\/heads\//, "");
    if (!name || !sha) return { status: 422, body: { message: "Invalid request" } };
    if (branches.has(name)) return { status: 422, body: { message: "Reference already exists" } };
    branches.set(name, sha);
    return { status: 201, body: { ref, object: { sha } } };
  }
  const read = url.pathname.match(new RegExp(`^${repo}/git/ref/heads/(.+)$`));
  if (req.method === "GET" && read) {
    const sha = branches.get(decodeURIComponent(read[1]));
    return sha ? { status: 200, body: { object: { sha } } } : { status: 404, body: { message: "Not Found" } };
  }
  const head = url.pathname.match(new RegExp(`^${repo}/commits/([^/]+)$`));
  if (req.method === "GET" && head) {
    const sha = branches.get(decodeURIComponent(head[1]));
    return sha ? { status: 200, body: { sha } } : { status: 404, body: { message: "Not Found" } };
  }
  return null;
}

// findRoute names the script entries that may answer a request, most specific
// first: `pull:7` before `pull`, so a test can script each pull request
// separately or give them all one answer. A pull request being opened is named
// by its head branch (`openPull:ticket-5-a2`), a comparison by its range
// (`compare:main...ticket-5-a2`).
function findRoute(method, path, body) {
  const repo = "/repos/[^/]+/[^/]+";
  const named = (name, ...specific) => ({ keys: [...specific.map((s) => `${name}:${decodeURIComponent(s)}`), name] });
  let m;
  if (path.endsWith("/access_tokens")) return named("token");
  if (/^\/app-manifests\/[^/]+\/conversions$/.test(path)) return named("conversion");
  if (path === "/app/installations") return named("installations");
  if (path === "/app/hook/config") return named(method === "PATCH" ? "hookPatch" : "hookConfig");
  if (path === "/installation/repositories") return named("repos");
  if (path === "/graphql") return named("graphql");
  if ((m = path.match(new RegExp(`^${repo}/pulls/(\\d+)/merge$`))) && method === "PUT") return named("merge", m[1]);
  if ((m = path.match(new RegExp(`^${repo}/pulls/(\\d+)$`)))) return named("pull", m[1]);
  if (method === "POST" && new RegExp(`^${repo}/pulls$`).test(path)) return named("openPull", JSON.parse(body || "{}").head ?? "");
  if ((m = path.match(new RegExp(`^${repo}/compare/(.+)$`)))) return named("compare", m[1]);
  if ((m = path.match(new RegExp(`^${repo}/commits/([^/]+)/check-runs$`)))) return named("checkRuns", m[1]);
  if ((m = path.match(new RegExp(`^${repo}/commits/([^/]+)/status$`)))) return named("commitStatus", m[1]);
  if ((m = path.match(new RegExp(`^${repo}/branches/(.+)$`)))) return named("branch", m[1]);
  if (new RegExp(`^${repo}$`).test(path)) return named("repo");
  return named("installation");
}

http
  .createServer(async (req, res) => {
    const url = new URL(req.url, "http://fake-github");
    const body = await readBody(req);
    if (url.pathname === "/_admin/script" && req.method === "POST") {
      script = JSON.parse(body);
      branches = new Map(Object.entries(script.branches ?? {}));
      requests = [];
      return reply(res, 200, {});
    }
    if (url.pathname === "/_admin/requests") return reply(res, 200, requests);
    if (url.pathname === "/healthz") return reply(res, 200, {});

    const web = webPage(req, url, body);
    if (web) {
      requests.push({ method: req.method, path: url.pathname, authorization: "", body });
      return web(res);
    }
    requests.push({ method: req.method, path: url.pathname, authorization: req.headers.authorization ?? "", body });
    const git = gitRoute(req, url, body);
    if (git) return reply(res, git.status, git.body);
    const match = findRoute(req.method, url.pathname, body);
    const answer = match.keys.map((key) => script[key]).find((scripted) => scripted);
    if (!answer) return reply(res, 501, { message: `nothing scripted for ${match.keys[0]}` });
    return reply(res, answer.status ?? 200, answer.body);
  })
  .listen(PORT, "0.0.0.0");
