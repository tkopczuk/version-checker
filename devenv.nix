# SANDBOX CONTRACT — read before editing this file or its imported modules.
# Full filesystem isolation is mandatory for Codex, its terminals, plugins,
# MCP servers and subprocesses. An ordinary devenv shell is not sandboxed.
# Required integrations: context-mode and ripwire, including MCP and skills.
# Runtime qualification remains outstanding until the actual launcher checks pass.
# SRT owns native enforcement; never replace it with an unsandboxed fallback,
# hand-written Seatbelt/Bubblewrap rules, containers or IDE approval settings.
# Missing enforcement must stop launch. Unverified integrations are not ready.
# SRT's required authenticated loopback proxies are allowed infrastructure.
# Application listeners remain prohibited; do not request approval for SRT's
# own proxy or expose it to the LAN. Policy changes require explicit approval.
#
# Approved policy:
#   IDE: Codex 0.154.0, SRT 0.0.76, context-mode 1.0.169, ripwire 0.6.1.
#     Native packages and integration are defined in nix/codex.nix.
#     codexdev disables Codex approvals and its nested sandbox (--yolo);
#     SRT enforces the outer boundary before Codex starts.
#   Tools: Go, gopls, Make, Helm, kubectl, Git; pinned Nixpkgs below.
#   Node and Bun are SRT/plugin runtimes, not project toolchains.
#   Writes: this checkout; private home, caches and state under .devenv/agent.
#   Reads: checkout, Nix store and essential operating-system runtime paths.
#     macOS configuration tree: /etc and /private/etc, read-only, matching the
#     supplied working example so absent Codex configuration probes return ENOENT.
#   Devices/integration: terminal and macOS com.apple.trustd.agent for Go TLS
#     certificate verification; no Docker socket or additional host home.
#   Credentials: fresh private Codex home; no host credentials imported.
#   Egress: any destination over SRT's TCP proxy; no direct TCP or UDP.
#   Application binding / host exposure: none, IPv4 and IPv6.
#   Bootstrap: dependency downloads allowed; agent/plugin setup must use SRT.
#   Enforcement: SRT library adapter in nix/codex.nix; no unsandboxed fallback.
#   Entry: devenv shell -- codexdev; checks: devenv shell -- devenv-check.
#   Native plugin setup: devenv tasks run agent:setup (also run by codexdev).
#
# Editing procedure:
# Use declarative Nix and native commands. Custom code requires a verified gap
# in supported tooling and must be limited to the smallest necessary adapter.
# Do not build custom installers, environment managers or test frameworks.
# 1. Maintain this contract. Edit devenv.nix and included .nix files only.
#    The user explicitly authorized the .devenv/ addition to .gitignore.
#    Keep YAML, locks, project instructions and standalone scripts untouched.
#    Generate helper scripts/configuration with Nix, not maintained side files.
# 2. Explain and obtain approval before broadening the policy above.
# 3. Keep pins explicit, bootstrap idempotent and secrets out of the Nix store.
#    Preserve existing inputs/locks; the reserved devenv input must be a flake.
# 4. In a disposable checkout, run devenv shell -- true, then
#    devenv shell -- codexdev --version and devenv shell -- devenv-check.
#    Verify both plugins through Codex, including their real tools and hooks;
#    parsing and version output alone do not establish readiness or isolation.
# 5. Restart the agent after changes to policy, packages or environment.
{ pkgs, ... }:
let
  # Pin the toolchain without changing devenv's bootstrap inputs or lockfile.
  tools = import (builtins.fetchTarball {
    url = "https://github.com/NixOS/nixpkgs/archive/a32edd7654519351e48e80372a928df336394670.tar.gz";
    sha256 = "0dc16crrqxp07nr2nwmv9ph30cb0nnq8zld5syq6p5f65c1pqd26";
  }) {
    system = pkgs.stdenv.hostPlatform.system;
    config.allowUnfreePredicate = package: (package.pname or "") == "context-mode";
  };
in
{
  imports = [ (import ./nix/codex.nix { inherit tools; }) ];
  packages = with tools; [ go gopls gnumake kubectl kubernetes-helm git ];

  # The repository uses pure Go builds and keeps its existing go.mod/go.sum.
  env.CGO_ENABLED = "0";
  env.GOTOOLCHAIN = "local";
  env.GOFLAGS = "-mod=readonly";

  enterShell = ''
    echo "Go / Make / gopls / Helm / kubectl are available."
    echo "Agent: codexdev (SRT sandbox). Checks: devenv-check."
  '';
}
