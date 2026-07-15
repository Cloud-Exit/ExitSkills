# Running ExitSkills

ExitSkills can run as a single Docker container with SQLite, as a Docker Compose stack with PostgreSQL, or in Kubernetes with the included Helm chart. Production deployments should use PostgreSQL and stable, randomly generated secrets.

## Required configuration

All deployment methods require:

- GitHub OAuth App `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`.
- A base64-encoded 32-byte `ENCRYPTION_KEY` that remains stable.
- A random `MASTER_TOKEN` of at least 32 bytes for administrative API access.
- Optionally, `AI_BASE_URL`, `AI_MODEL`, and, when required by the provider, `AI_API_KEY`.

Generate the local secret values with:

```sh
openssl rand -base64 32
openssl rand -hex 32
```

The first value is the encryption key; the second is suitable as the master token.

## Docker

Build the Alpine-based image:

```sh
docker build --build-arg VERSION=dev -t exitskills:local .
```

The Dockerfile and Compose PostgreSQL image retain readable release tags while pinning their reviewed multi-platform digests. Update each tag and digest together rather than removing the digest lock.

Copy the container environment template, fill in the required values, and use the embedded SQLite backend:

```sh
cp .env.docker.example .env.docker
docker volume create exitskills-data
docker run --rm --name exitskills \
  --env-file .env.docker \
  -e DATABASE_URL=sqlite:///data/skills.db \
  -e LISTEN_ADDRESS=:8080 \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  -p 127.0.0.1:8111:8080 \
  -v exitskills-data:/data \
  exitskills:local
```

The API is available at `http://localhost:8111`; Redoc is at `http://localhost:8111/v1/docs`. Create a client API key in another terminal:

```sh
docker exec exitskills admin --name local-client --valid-for 24h
```

## Docker Compose

The included Compose stack builds ExitSkills and starts PostgreSQL 18 with a persistent named volume:

```sh
cp .env.docker.example .env.docker
# Fill in POSTGRES_PASSWORD, GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET,
# ENCRYPTION_KEY, and MASTER_TOKEN.
docker compose --env-file .env.docker up --build -d
docker compose --env-file .env.docker logs -f exitskills
```

Compose requires an explicit PostgreSQL password, binds the API to `127.0.0.1` by default, and confines the application container with a read-only root filesystem, dropped capabilities, and `no-new-privileges`. Put a TLS reverse proxy in front of the service before exposing it beyond the local host.

Create an API key and check readiness:

```sh
docker compose --env-file .env.docker exec exitskills \
  admin --name local-client --valid-for 24h
curl http://localhost:8111/readyz
```

Stop the stack without deleting PostgreSQL data:

```sh
docker compose --env-file .env.docker down
```

Add `--volumes` only when the stored catalog and API keys should be deleted.

## Kubernetes with Helm

The Helm chart deploys ExitSkills but deliberately does not install PostgreSQL. Provision PostgreSQL first and obtain its connection URL. Then create the namespace and application Secret:

```sh
kubectl create namespace exitskills
kubectl -n exitskills create secret generic exitskills-secrets \
  --from-literal=DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/exitskills?sslmode=require' \
  --from-literal=GITHUB_CLIENT_ID='YOUR_CLIENT_ID' \
  --from-literal=GITHUB_CLIENT_SECRET='YOUR_CLIENT_SECRET' \
  --from-literal=ENCRYPTION_KEY='BASE64_32_BYTE_KEY' \
  --from-literal=MASTER_TOKEN='RANDOM_MASTER_TOKEN' \
  --from-literal=AI_API_KEY='YOUR_AI_API_KEY'
```

Install the published OCI Helm chart. Pin an explicit SemVer in production:

```sh
helm upgrade --install exitskills oci://ghcr.io/cloud-exit/exitmesh-skills \
  --version 0.2.0 \
  --namespace exitskills \
  --set fullnameOverride=exitskills \
  --set existingSecret=exitskills-secrets \
  --set image.tag=0.2.0
```

Inspect the rollout, access the API locally, and create a client key using the bundled `admin` executable:

```sh
kubectl -n exitskills rollout status deployment/exitskills
kubectl -n exitskills port-forward service/exitskills 8111:80
kubectl -n exitskills exec deployment/exitskills -- \
  admin --name automation --valid-for 720h
```

The liveness and readiness endpoints are `/healthz` and `/readyz`. The chart defaults to one replica because API rate limiting is process-local; PostgreSQL supports multiple replicas, but each pod maintains an independent rate-limit window.

## Upgrades and backups

Database migrations run automatically when the server or `admin` starts. When both AI settings are configured, boot assesses every stored unchecked skill before the API starts and before GitHub discovery; only explicit passes remain. Omit both settings to skip all LLM processing and publish discovered skills with `llmChecked: false` and no implied scores. Back up PostgreSQL before upgrades and keep `ENCRYPTION_KEY` unchanged, because changing it invalidates stored API-key authentication. For SQLite deployments, stop the container and back up the Docker volume before replacing the image.
