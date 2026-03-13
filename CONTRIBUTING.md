# Contributing

Thank you for your interest in contributing to the x402 reference implementation.

## Before You Start

This project follows a **specification-first** model. The canonical protocol specification lives in the [merkleworks-x402-spec](https://github.com/ruidasilva/merkleworks-x402-spec) repository. Implementation changes must not contradict the specification.

For questions about contribution scope, see [GOVERNANCE.md](GOVERNANCE.md).

## Development Setup

### Prerequisites

- Go 1.25+
- Node.js 20+ (for dashboard)
- Docker and Docker Compose (optional, for containerized deployment)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/merkleworks/x402-bsv.git
cd x402-bsv

# Generate a testnet key
go run ./cmd/keygen

# Run tests
make test

# Run linter
make lint

# Build all binaries and dashboard
make build

# Start in demo mode (auto-seeds UTXO pools)
make demo
```

## Making Changes

### 1. Open an Issue First

- **Bug fixes**: Describe the bug and how to reproduce it
- **Enhancements**: Propose the change and wait for discussion before writing code
- **New features**: Confirm alignment with the specification before implementation

### 2. Create a Branch

```bash
git checkout -b fix/description   # for bug fixes
git checkout -b feat/description  # for features
```

### 3. Write Code

- Follow existing code patterns and conventions
- Add tests for new functionality
- Keep changes focused — one concern per pull request
- Include Apache 2.0 license headers on new source files

### 4. Test

```bash
# Go tests
make test

# Go vet
make lint

# Dashboard type-check and build
cd dashboard && npm run build
```

### 5. Submit a Pull Request

- Reference the related issue
- Describe what changed and why
- Ensure all CI checks pass

## Code Style

- **Go**: Standard `gofmt` formatting, `go vet` must pass
- **TypeScript**: Follow existing patterns in `dashboard/` and `client-js/`
- **Commits**: Use conventional prefixes (`fix:`, `feat:`, `docs:`, `chore:`)

## Pull Request Review

Pull requests require at least one maintainer approval. See [MAINTAINERS.md](MAINTAINERS.md) for the current maintainer list.

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
