# Contributing to ExitSkills

ExitSkills is free and open-source software maintained by Cloud Exit B.V. Contributions are welcome through issues and pull requests.

## Development setup

Install Go 1.23 and Helm, clone the repository, and prepare local configuration:

```sh
cp .env.example .env
make test
make run
```

`make run` uses SQLite at `.local/skills.db` and immediately starts an indexing refresh. Docker is optional for Go development.

## Development workflow

TDD is mandatory:

1. Add or update a test that demonstrates the missing behavior and confirm it fails.
2. Implement the smallest complete change that makes it pass.
3. Refactor while keeping the suite green.

Keep changes focused. Preserve compatibility with both SQLite and PostgreSQL when modifying persistence. Never log credentials, skill contents, API keys, authorization headers, or private user data.

Before opening a pull request, run:

```sh
make test-race
make vet
make fmt-check
make build
make helm-lint
make docs
```

Container-related changes should also be built and exercised locally when a container runtime is available.

## Documentation and APIs

Update `README.md` whenever behavior, configuration, setup, or operations change. Update `docs/openapi.json` whenever an API route, request, response, authentication rule, or status code changes, then regenerate the Redoc artifact with `make docs`.

## Pull requests

Describe the problem, the chosen behavior, and how it was tested. Link related issues and call out migrations, compatibility concerns, or operational changes. Pull requests must pass the repository's formatting, vet, race-test, build, and Helm checks.

For security vulnerabilities, prefer GitHub's private vulnerability reporting rather than a public issue when it is available for the repository.

## License

By contributing, you agree that your contribution is licensed under the repository's MIT License. Copyright for the project is held by Cloud Exit B.V. as stated in `LICENSE`.
