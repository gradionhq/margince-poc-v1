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
#
# VERSION is the RELEASE version, and this bake is the only place it becomes
# one value for the whole set. It is the tag, the OCI version label and the
# MARGINCE_RELEASE_VERSION build arg, all from the same variable, because a
# customer pulls each role by tag and two tag pulls are two requests: a publish
# racing those pulls can serve a set whose roles come from different releases,
# and the OCI protocol gives the registry no way to refuse that at the pull.
# What the roles then refuse is the RUN — so every role has to carry the
# version it was built from, in the image and in the binary alike.

# The constellation registry namespace the publisher grant admits
# (registryauth catalog: push on margince/*). The release workflow pushes the
# baked images here.
variable "REPO" {
  default = "registry.test.margince.com/margince"
}

# The release this set is built from — the version the release workflow drafted
# (`1970.<build>`, the constellation YYYY.edition scheme). "dev" is the local
# default and is deliberately the same string internal/shared/buildinfo calls
# Unknown: a local build has no release, and a comparison against one would be
# a difference that means nothing.
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
# ignore-error=true on every export: the cache is an optimization, and a cache
# outage must cost the release a cold bake, never the release itself (the
# exporter default fails the whole bake on an export error).
variable "CACHE" {
  default = ""
}

# Every role lives in the ONE root Dockerfile as a build target of the same
# name; the shared Go builder base is spelled once there and built once per
# bake. A deploy recipe building a role directly says
# `docker build --target <role> .` (a d13 dockerBuild block: `dockerFile:
# ./Dockerfile` + `arguments: ["--target", "<role>"]` — d13 has no target
# key, only pass-through arguments).
#
# The version label and the MARGINCE_RELEASE_VERSION arg are declared HERE, on
# the shared target, rather than per role: the whole point is that the three
# roles carry the SAME release version, and three separate declarations are
# three chances for them not to.
target "role" {
  context    = "."
  dockerfile = "Dockerfile"
  platforms  = PLATFORMS == "" ? [] : split(",", PLATFORMS)
  args = {
    MARGINCE_BUILD_REVISION  = MARGINCE_BUILD_REVISION
    MARGINCE_RELEASE_VERSION = VERSION
  }
  # The canonical OCI annotation, so `docker inspect` / `crane config` answers
  # the release without running the image — which is the only way to read it
  # off the web image, whose runtime is nginx and runs none of our code.
  labels = {
    "org.opencontainers.image.version" = VERSION
  }
}

target "api" {
  inherits   = ["role"]
  target     = "api"
  tags       = ["${REPO}/api:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-api"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-api,mode=max,ignore-error=true"] : []
}

target "web" {
  inherits   = ["role"]
  target     = "web"
  tags       = ["${REPO}/web:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-web"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-web,mode=max,ignore-error=true"] : []
}

target "worker" {
  inherits   = ["role"]
  target     = "worker"
  tags       = ["${REPO}/worker:${VERSION}"]
  cache-from = CACHE == "gha" ? ["type=gha,scope=margince-worker"] : []
  cache-to   = CACHE == "gha" ? ["type=gha,scope=margince-worker,mode=max,ignore-error=true"] : []
}
