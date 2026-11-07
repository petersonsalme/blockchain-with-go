# ADR-0036: Signed release with SBOM and a published image

Status: Accepted (ENG-356).

## Context
- `release.yml` publishes binaries and `checksums.txt` on `v*` tags with goreleaser, but a binary cannot say which version it is, nothing says what is inside it, nobody can prove the checksums came from this repository, and the only image is built locally from `Dockerfile`.
- ADR-0021 lists release tooling among the market tools to use around the hand-written core.

## Options
1. **Keep the current release.** Cost: nothing; the release stays unverifiable and users build the image themselves.
2. **GPG-signed checksums, image built by a separate workflow.** Cost: a long-lived private key kept as a repository secret and rotated by hand; two pipelines that can drift in tag and content.
3. **goreleaser with syft SBOMs, cosign keyless signatures and `dockers_v2` to GHCR (chosen).** Cost: three CI-only tools (syft, cosign, buildx/QEMU) and a slower release; signatures depend on the public Sigstore instance (Fulcio, Rekor).

## Decision
- `bwg version` prints the tag, the full commit and the build date injected with `-X` ldflags; builds without them print `dev`/`none`/`unknown`.
- goreleaser generates one syft SBOM per archive, signs `checksums.txt` with cosign keyless through the workflow's GitHub OIDC token, and builds and pushes `ghcr.io/petersonsalme/blockchain-with-go` (linux/amd64, linux/arm64; tags `<tag>` and `latest`) from `Dockerfile.release`, signed by digest the same way.
- ADR-0021 criteria: (1) build and release tooling only, nothing in the core; (2) goreleaser, syft and cosign (Sigstore, OpenSSF) are the de facto standards for Go releases, maintained, Apache-2.0; (3) they run as CI tools, never linked; (4) `go.mod` gains no module, `govulncheck` is unaffected; (5) hand-written signing or SBOM generation teaches nothing about blockchains.

## Consequences
- A user verifies a download with `cosign verify-blob` against `checksums.txt.sigstore.json` and the image with `cosign verify`, both pinned to the `release.yml` identity on `refs/tags/v*`; no key to distribute or rotate.
- The PR CI builds a snapshot (SBOMs and images, no publish, no signature), so a broken release config fails before the tag.
- The GHCR package starts private; the board makes it public once, in the package settings.
- The signature proves where an artifact was built, not that it is safe: the project stays not for production (ADR-0021).

## Files
`cmd/bwg/version.go` (new), `cmd/bwg/version_test.go` (new), `cmd/bwg/main.go`, `.goreleaser.yaml`, `Dockerfile.release` (new), `.github/workflows/release.yml`, `.github/workflows/go-ci.yml`, `docs/adr/0036-signed-release-sbom-image.md` (new), `README.md`, `AGENTS.md`.

## Out of scope
- Pushing a tag or publishing a release (board action after merge; making the GHCR package public is the board's too).
- SLSA provenance attestations, Homebrew/apt packaging, signing individual binaries.
- Changing `Dockerfile`, `docker-compose.yml`, the e2e or the `bwg node` behaviour; a `--version` flag; exposing the version over HTTP.
