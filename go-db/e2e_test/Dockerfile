FROM python:3.12-slim

RUN pip install --no-cache-dir uv

WORKDIR /srv

COPY pyproject.toml ./
RUN uv sync

COPY . .

# Playwright's API request context (used here instead of a browser page,
# since demo-api has no UI) doesn't need a browser installed.
CMD ["uv", "run", "pytest", "-v"]
