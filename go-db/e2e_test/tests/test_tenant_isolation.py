import uuid

from playwright.sync_api import APIRequestContext


def create_tenant(api: APIRequestContext, name: str) -> str:
    resp = api.post("/tenants", data={"name": name})
    assert resp.ok, resp.text()
    return resp.json()["id"]


def test_healthz(api: APIRequestContext):
    resp = api.get("/healthz")
    assert resp.ok


def test_tenant_creation_and_listing(api: APIRequestContext):
    name = f"tenant-{uuid.uuid4()}"
    create_tenant(api, name)

    resp = api.get("/tenants")
    assert resp.ok
    names = [t["name"] for t in resp.json()]
    assert name in names


def test_missing_tenant_header_is_rejected(api: APIRequestContext):
    resp = api.get("/accounts")
    assert resp.status == 400


def test_invalid_tenant_header_is_rejected(api: APIRequestContext):
    resp = api.get("/accounts", headers={"X-Tenant-ID": "not-a-uuid"})
    assert resp.status == 400


def test_cross_tenant_isolation(api: APIRequestContext):
    """The core end-to-end proof: two tenants, one account each, and each
    tenant sees only its own account -- enforced by RLS through the real
    HTTP -> tenancy -> engine -> Postgres stack, not an application filter.
    """
    tenant_a = create_tenant(api, f"tenant-a-{uuid.uuid4()}")
    tenant_b = create_tenant(api, f"tenant-b-{uuid.uuid4()}")

    resp = api.post(
        "/accounts",
        headers={"X-Tenant-ID": tenant_a},
        data={"name": "acc-a", "currency_code": "USD"},
    )
    assert resp.ok, resp.text()

    resp = api.get("/accounts", headers={"X-Tenant-ID": tenant_a})
    assert resp.ok
    accounts_a = resp.json()
    assert len(accounts_a) == 1
    assert accounts_a[0]["tenant_id"] == tenant_a

    resp = api.get("/accounts", headers={"X-Tenant-ID": tenant_b})
    assert resp.ok
    assert resp.json() == []
