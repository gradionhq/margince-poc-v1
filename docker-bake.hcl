# Bake definition for the three role images (api, web, worker). The release
# workflow builds and pushes all three through this file in one `docker buildx
# bake` invocation; a plain `docker buildx bake` builds them locally with the
# defaults below (no push, throwaway tag).
#
# Every variable arrives from the environment. MARGINCE_BUILD_REVISION is ONE
# revision for every role, so the api and the web tier can be compared at run
# time; a Dockerfile that does not declare the ARG simply ignores it. Absent
# (a local build) it stays empty, which disables the comparison rather than
# alarming on it.

# The constellation registry namespace the publisher grant admits
# (registryauth catalog: push on margince/*). The release workflow pushes the
# baked images here.
variable "REPO" {
  default = "registry.test.margince.com/margince"
}

variable "VERSION" {
  default = "dev"
}

variable "MARGINCE_BUILD_REVISION" {
  default = ""
}

group "default" {
  targets = ["api", "web", "worker"]
}

# Comma-separated target platforms. Empty means the invoker's native platform,
# which the default docker driver can build — the release workflow sets
# linux/amd64,linux/arm64 (and full provenance via --provenance mode=max, an
# invoker flag because attestations also exceed the docker driver).
variable "PLATFORMS" {
  default = ""
}

# "gha" exports/imports the layer cache through the GitHub Actions cache, one
# scope per role. Only the release workflow sets it: type=gha needs the Actions
# runtime credentials in the builder's environment (release.yml exposes them),
# so anywhere else the empty default keeps the bake self-contained. mode=max
# exports the builder stages too — the dependency-download layer is the one
# worth the upload, the source-dependent layers after it miss on every commit.
variable "CACHE" {
  default = ""
}

target "role" {
  context   = "."
  platforms = PLATFORMS == "" ? [] : split(",", PLATFORMS)
  args = {
    MARGINCE_BUILD_REVISION = MARGINCE_BUILD_REVISION
  }
}

target "api" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.api"
  tags       = ["${REPO}/api:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-api"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-api,mode=max"] : []
}

target "web" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.web"
  tags       = ["${REPO}/web:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-web"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-web,mode=max"] : []
}

target "worker" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.worker"
  tags       = ["${REPO}/worker:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-worker"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-worker,mode=max"] : []
}
