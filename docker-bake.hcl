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

target "role" {
  context = "."
  # A plain single manifest per image, no provenance attestation: the
  # constellation registry records one manifest digest per role (the tag
  # resolver serves virtual tags over it), and an attestation turns the push
  # into an OCI index whose child manifests land milliseconds before the
  # index — a read-after-write window the registry's replicated deployment
  # does not serve coherently.
  provenance = false
  args = {
    MARGINCE_BUILD_REVISION = MARGINCE_BUILD_REVISION
  }
}

target "api" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.api"
  tags       = ["${REPO}/api:${VERSION}"]
}

target "web" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.web"
  tags       = ["${REPO}/web:${VERSION}"]
}

target "worker" {
  inherits   = ["role"]
  dockerfile = "Dockerfile.worker"
  tags       = ["${REPO}/worker:${VERSION}"]
}
