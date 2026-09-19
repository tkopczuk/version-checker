{ tools }:
{ config, lib, ... }:
let
  root = config.devenv.root;
  state = "${root}/.devenv/agent";
  srt = tools.sandbox-runtime.overrideAttrs (old: {
    # 0.0.76 advertises localhost, but Node cannot resolve it with root-denied
    # reads. The proxy already binds 127.0.0.1; advertise that same endpoint.
    # Verify with the npm install inside `devenv shell -- codexdev`.
    postInstall = (old.postInstall or "") + ''
      substituteInPlace "$out/lib/node_modules/@anthropic-ai/sandbox-runtime/dist/sandbox/sandbox-utils.js" \
        --replace-fail '}localhost:' '}127.0.0.1:'
    '';
  });
  codex = tools.codex; # Includes codex-code-mode-host, not just the CLI binary.
  contextMode = tools.context-mode.overrideAttrs {
    version = "1.0.169";
    src = tools.fetchurl {
      url = "https://registry.npmjs.org/context-mode/-/context-mode-1.0.169.tgz";
      hash = "sha256-CcQeTPd7IVZsdrjqL9vX89gjBV/uLwLCFm/Vu1ddryw=";
    };
    # 1.0.169's shipped executor overrides TMPDIR. Honor the approved directory
    # first without altering its environment denylist. Verify with ctx_execute.
    postPatch = ''
      substituteInPlace server.bundle.mjs --replace-fail \
        'var Rj=(()=>{' 'var Rj=(()=>{if(process.env.TMPDIR)return process.env.TMPDIR;'
      substituteInPlace .codex-plugin/hooks.json --replace-fail \
        '"command": "node ' '"command": "${tools.bun}/bin/bun '
    '';
    # Exclude other platforms' examples containing SRT-protected .mcp.json.
    # Bun supplies SQLite; npm installs the JS dependencies in the setup task.
    installPhase = ''
      mkdir -p "$out/lib/context-mode" "$out/bin"
      cp -R package.json server.bundle.mjs cli.bundle.mjs start.mjs \
        .codex-plugin hooks scripts skills bin "$out/lib/context-mode/"
      makeWrapper ${tools.bun}/bin/bun "$out/bin/context-mode" \
        --add-flags "$out/lib/context-mode/server.bundle.mjs"
    '';
  };
  ripwire = tools.ripwire.overrideAttrs (old: {
    version = "0.6.1";
    src = tools.fetchFromGitHub {
      owner = "redhat-et";
      repo = "ripwire";
      tag = "v0.6.1";
      hash = "sha256-2a4J9lS0rdJyhkXkpQSkFrSf+NsMyeI2mJJNIYvgA8Y=";
    };
    # 0.6.1 checks C++ IPO too; the inherited Darwin hook only configures C.
    preConfigure = (old.preConfigure or "") + lib.optionalString tools.stdenv.hostPlatform.isDarwin ''
      prependToVar cmakeFlags "-DCMAKE_CXX_COMPILER_AR=$(command -v "$AR")"
      prependToVar cmakeFlags "-DCMAKE_CXX_COMPILER_RANLIB=$(command -v "$RANLIB")"
    '';
  });
  directories = {
    HOME = "${state}/home";
    CODEX_HOME = "${state}/codex";
    TMPDIR = "${state}/tmp";
    XDG_CACHE_HOME = "${state}/cache";
    XDG_CONFIG_HOME = "${state}/config";
    XDG_DATA_HOME = "${state}/data";
    XDG_STATE_HOME = "${state}/state";
    CONTEXT_MODE_DIR = "${state}/context";
    RIPWIRE_HOME = "${state}/ripwire";
    GOPATH = "${state}/go";
    GOMODCACHE = "${state}/go-modules";
    GOCACHE = "${state}/go-cache";
  };
  environment = directories // {
    PATH = lib.makeBinPath (with tools; [
      codex ripwire bun nodejs bashInteractive coreutils findutils gnugrep
      gnused git curl ripgrep which jq go gopls gnumake kubectl kubernetes-helm
    ]);
    SHELL = "${tools.bashInteractive}/bin/bash";
    CONTEXT_MODE_PLATFORM = "codex";
    CONTEXT_MODE_PROJECT_DIR = root;
    CGO_ENABLED = "0";
    GOTOOLCHAIN = "local";
    GOFLAGS = "-mod=readonly";
    GOTMPDIR = directories.TMPDIR;
    # SRT 0.0.76 overrides TMPDIR; this selects the approved private directory.
    CLAUDE_CODE_TMPDIR = directories.TMPDIR;
    SSL_CERT_FILE = "${tools.cacert}/etc/ssl/certs/ca-bundle.crt";
    NODE_EXTRA_CA_CERTS = "${tools.cacert}/etc/ssl/certs/ca-bundle.crt";
    OPENSSL_CONF = "${tools.writeText "openssl.cnf" ""}";
    LANG = "en_US.UTF-8";
  };
  inherited = builtins.attrNames environment ++ [
    "HTTP_PROXY" "HTTPS_PROXY" "ALL_PROXY" "NO_PROXY"
    "http_proxy" "https_proxy" "all_proxy" "no_proxy"
  ];
  json = name: value: tools.writeText name (builtins.toJSON value);
  policy = json "srt-policy.json" {
    filesystem = {
      denyRead = [ "/" "${root}/.env" "${root}/.envrc" ];
      allowRead = [ root "/nix/store" "/bin" "/usr/bin" "/usr/lib" "/System/Library"
        # Match the working macOS example: Codex probes fixed paths even when
        # /etc/codex is absent, so Seatbelt needs the existing configuration tree.
        "/etc" "/private/etc"
        "/dev/null" "/dev/zero" "/dev/random" "/dev/urandom" "/dev/tty" "/dev/ttys*" "/dev/ptmx" ];
      allowWrite = [ root ];
      denyWrite = [ ];
    };
    network = {
      allowedDomains = [ ];
      deniedDomains = [ ];
      strictAllowlist = false; # The library callback below grants all TCP destinations.
      allowLocalBinding = false;
      # Go 1.26 uses macOS trust evaluation, even with SSL_CERT_FILE set.
      # Without this service, module downloads fail with x509: OSStatus -26276.
      allowMachLookup = [ "com.apple.trustd.agent" ];
      allowAllUnixSockets = false;
      allowUnixSockets = [ directories.TMPDIR ]; # Private agent IPC, no host sockets.
    };
    allowPty = true;
    ripgrep.command = "${tools.ripgrep}/bin/rg";
    enableWeakerNetworkIsolation = false;
    enableWeakerNestedSandbox = false;
  };
  # Necessary adapter: SRT 0.0.76's CLI cannot provide its documented all-host
  # permission callback. Its library takes a shell command, hence argument quoting.
  # OS enforcement, proxy authentication and lifecycle remain owned by SRT.
  adapter = tools.writeText "srt-codex.mjs" ''
    import { readFileSync } from 'node:fs';
    import { spawn } from 'node:child_process';
    import { SandboxManager as srt, SandboxRuntimeConfigSchema } from '${srt}/lib/node_modules/@anthropic-ai/sandbox-runtime/dist/index.js';
    if (process.platform !== 'darwin') throw new Error('This application-binding policy requires macOS.');
    const dependencies = await srt.checkDependenciesAsync({ command: '${tools.ripgrep}/bin/rg' });
    if (dependencies.errors.length) throw new Error(dependencies.errors.join('; '));
    const policy = SandboxRuntimeConfigSchema.parse(JSON.parse(readFileSync('${policy}', 'utf8')));
    const quote = value => "'" + value.replaceAll("'", "'\"'\"'") + "'";
    try {
      await srt.initialize(policy, async () => true, false);
      const { argv, env } = await srt.wrapWithSandboxArgv(process.argv.slice(2).map(quote).join(' '), '${tools.bashInteractive}/bin/bash');
      const child = spawn(argv[0], argv.slice(1), { env, stdio: 'inherit', cwd: ${builtins.toJSON root} });
      process.on('SIGINT', () => child.kill('SIGINT'));
      process.on('SIGTERM', () => child.kill('SIGTERM'));
      process.exitCode = await new Promise((resolve, reject) => {
        child.once('error', reject);
        child.once('exit', code => resolve(code ?? 1));
      });
    } finally { await srt.reset(); }
  '';
  run = tools.writeShellScript "version-checker-sandbox" ''
    set -euo pipefail
    # Check each parent before creating its children; never follow state symlinks.
    for directory in ${lib.escapeShellArgs ([ "${root}/.devenv" state ] ++ builtins.attrValues directories)}; do
      test ! -L "$directory" || { echo "Refusing symlink: $directory" >&2; exit 1; }
      ${tools.coreutils}/bin/mkdir -p -m 700 "$directory"
    done
    exec ${tools.coreutils}/bin/env -i \
      ${lib.escapeShellArgs (lib.mapAttrsToList (key: value: "${key}=${value}") environment)} \
      TERM="''${TERM:-xterm-256color}" ${tools.nodejs}/bin/node ${adapter} "$@"
  '';
  contextMcp = json "context-mode-mcp.json" {
    mcpServers.context-mode = {
      command = "${tools.bun}/bin/bun";
      args = [ "./server.bundle.mjs" ];
      cwd = ".";
      env_vars = inherited;
    };
  };
  ripwireMcp = json "ripwire-mcp.json" {
    mcpServers.ripwire = {
      command = "${ripwire}/bin/ripwire";
      args = [ "--mcp" ];
      env_vars = inherited;
    };
  };
  ripwireHooks = json "ripwire-hooks.json" {
    hooks = {
      SessionStart = [{ hooks = [{ type = "command"; command = "bash \"\${PLUGIN_ROOT}/hooks/ripwire-codex-nudge.sh\" --session-start"; }]; }];
      UserPromptSubmit = [{ hooks = [{ type = "command"; command = "bash \"\${PLUGIN_ROOT}/hooks/ripwire-codex-route.sh\""; }]; }];
      PreToolUse = [{ hooks = [{ type = "command"; command = "bash \"\${PLUGIN_ROOT}/hooks/ripwire-codex-nudge.sh\""; }]; }];
    };
  };
  marketplace = tools.runCommand "version-checker-plugins" { nativeBuildInputs = [ tools.jq ]; } ''
    mkdir -p "$out/context-mode" "$out/ripwire/.codex-plugin" "$out/.agents/plugins"
    cp -R ${contextMode}/lib/context-mode/. "$out/context-mode/"
    chmod -R u+w "$out"
    cp ${contextMcp} "$out/context-mode/.codex-plugin/mcp.json"
    cp -R ${ripwire.src}/skills ${ripwire.src}/hooks "$out/ripwire/"
    jq '.mcpServers = "./.codex-plugin/mcp.json" | .hooks = "./.codex-plugin/hooks.json"' \
      ${ripwire.src}/.codex-plugin/plugin.json > "$out/ripwire/.codex-plugin/plugin.json"
    cp ${ripwireMcp} "$out/ripwire/.codex-plugin/mcp.json"
    cp ${ripwireHooks} "$out/ripwire/.codex-plugin/hooks.json"
    cp ${json "marketplace.json" {
      name = "version-checker-devenv";
      plugins = map (name: {
        inherit name;
        source = { source = "local"; path = "./${name}"; };
        policy = { installation = "AVAILABLE"; authentication = "ON_INSTALL"; };
        category = "Developer Tools";
      }) [ "context-mode" "ripwire" ];
    }} "$out/.agents/plugins/marketplace.json"
  '';
  codexConfig = tools.writeText "codex-config.toml" ''
    # SRT enforces the outer boundary; preserve its cleaned, proxied environment.
    sandbox_mode = "danger-full-access"
    developer_instructions = "Use ripwire for code navigation and context-mode for substantial command output and searchable context. Application listeners are prohibited; SRT's internal loopback proxy is allowed infrastructure."
    [shell_environment_policy]
    inherit = "all"
    [features]
    hooks = true
    plugin_hooks = true
  '';
  setup = tools.writeShellScript "register-codex-plugins" ''
    set -euo pipefail
    mkdir -p ${lib.escapeShellArg "${state}/plugins"}
    cp -R ${marketplace}/. ${lib.escapeShellArg "${state}/plugins"}
    chmod -R u+w ${lib.escapeShellArg "${state}/plugins"}
    npm install --prefix ${lib.escapeShellArg "${state}/plugins/context-mode"} \
      --omit=dev --ignore-scripts --no-audit --no-fund --package-lock=false \
      --fetch-retries=0 --fetch-timeout=20000 --loglevel=http
    test -f "$CODEX_HOME/config.toml" || install -m 600 ${codexConfig} "$CODEX_HOME/config.toml"
    codex plugin marketplace add ${lib.escapeShellArg "${state}/plugins"} --json
    codex plugin add context-mode@version-checker-devenv --json
    codex plugin add ripwire@version-checker-devenv --json
  '';
  check = tools.writeShellScript "check-codex-environment" ''
    set -euo pipefail
    trap 'echo "devenv-check failed (line $LINENO): $BASH_COMMAND" >&2' ERR
    echo "Checking SRT sandbox..."
    test -w "$HOME"
    test -w "$CODEX_HOME"
    test -w "$TMPDIR"
    if cat ${lib.escapeShellArg "${root}/../AGENTS.md"} >/dev/null 2>&1; then
      echo "Sandbox isolation failed: the parent AGENTS.md is readable." >&2
      exit 1
    fi
    go version
    echo "Checking Go TLS with a fresh module cache..."
    tls_cache="$(mktemp -d "$TMPDIR/go-tls.XXXXXX")"
    trap 'rm -rf "$tls_cache"' EXIT
    GOMODCACHE="$tls_cache" timeout 30s go list -m -json github.com/stretchr/testify@v1.12.1 >/dev/null
    codex --version
    ripwire --version
    curl --silent --show-error --max-time 20 --output /dev/null https://auth.openai.com/
    codex plugin list --marketplace version-checker-devenv --json
    echo "Basic checks passed; agent-mediated MCP, hook and isolation qualification is separate."
  '';
in
{
  tasks."agent:setup".exec = "${run} ${setup}";
  scripts.codexdev.exec = ''
    set -euo pipefail
    ${run} ${setup}
    exec ${run} ${codex}/bin/codex --dangerously-bypass-approvals-and-sandbox "$@"
  '';
  scripts.devenv-check.exec = ''
    set -euo pipefail
    ${run} ${setup}
    exec ${run} ${check}
  '';
}
