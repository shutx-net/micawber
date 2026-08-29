{
  description = "Micawber -- a portable, Git-native headless CMS";

  inputs = {
    # Channel tarball rather than `github:NixOS/nixpkgs/nixos-26.05`: some networks block
    # api.github.com, and that is the only host nix's `github:` fetcher will talk to.
    # channels.nixos.org redirects to an immutable releases.nixos.org snapshot, so
    # flake.lock still pins one exact nixpkgs revision.
    nixpkgs.url = "https://channels.nixos.org/nixos-26.05/nixexprs.tar.xz";
  };

  outputs =
    { nixpkgs, ... }:
    let
      inherit (nixpkgs) lib;

      # Portability is a product requirement, so the shell is declared for every platform
      # Micawber is meant to build on, not just the one it was written on.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      # No flake-utils input: lib.genAttrs from the pinned nixpkgs does the same job
      # without a second flake to lock, update and trust.
      forAllSystems = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        # mkShell rather than mkShellNoCC: `go test -race` needs cgo, and it is stdenv
        # that puts a C compiler on PATH.
        default = pkgs.mkShell {
          name = "micawber";

          packages = [
            # gofmt, go vet and go test ship inside this, so it covers the tooling
            # AGENTS.md asks for on its own.
            pkgs.go
            # Language server, for editors and coding agents.
            pkgs.gopls
            # The usual lint gate. With no .golangci.yml yet it runs its own defaults;
            # add the config when there is code worth configuring it for.
            pkgs.golangci-lint
            # Micawber drives *the user's* git rather than linking a Git library, so from
            # the Git-backed content operations onward the test suite cannot run without a
            # git binary, and a shell that cannot run the tests is not a dev shell. This
            # one is pinned so those tests are reproducible across machines.
            #
            # It is a build-and-test dependency and nothing more. It is not, and must not
            # become, an assumption about which git a deployed Micawber drives: that stays
            # whatever is on the host's PATH, or whatever WithGitBinary names.
            #
            # The original reason for leaving it out still holds and is worth keeping: a
            # nixpkgs git on PATH shadows the user's own, and with it the credential helper
            # and ssh config that talking to a remote depends on. That is harmless here --
            # the tests are hermetic and touch no remote -- and it becomes live again at
            # the remote phase, where the answer is configuration rather than an empty
            # shell.
            pkgs.git
          ];

          # Pin the toolchain to the one in this shell. Under the default GOTOOLCHAIN=auto
          # the go command downloads and runs a different Go whenever go.mod names a newer
          # version, which would make the pin above a suggestion. `local` turns that case
          # into an error instead; override per command with `GOTOOLCHAIN=auto go ...`.
          GOTOOLCHAIN = "local";

          # CGO_ENABLED is deliberately not set here. The standalone binary AGENTS.md wants
          # is built with CGO_ENABLED=0, but setting that shell-wide would also disable
          # `go test -race`. It belongs on the build command -- see the banner.
          shellHook = ''
            echo "micawber dev shell"
            echo "  go     : $(go version | cut -d' ' -f3)  (GOTOOLCHAIN=$GOTOOLCHAIN)"
            echo "  lint   : golangci-lint run"
            echo "  release: CGO_ENABLED=0 go build ./...   # static, dependency-free"
          '';
        };
      });

      # Used by `nix fmt`. nixfmt-tree rather than bare nixfmt: `nix fmt` with no arguments
      # passes none on to the formatter, and nixfmt then reads stdin instead of the tree.
      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);

      # packages.default (buildGoModule) belongs here once there is a main package: it
      # needs a go.mod and a vendorHash, so today it could only fail.
    };
}
