{
  description = "cbztool — a fast, secure, zero-dependency CLI (cbz) for .cbz comic archives";

  # Single input by design: cbztool is zero-dependency, and the flake keeps that
  # spirit — nixpkgs is the only thing we pull in (no flake-utils, etc.).
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      # Systems we build for. Trim or extend as needed.
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      # forAllSystems f -> { <system> = f pkgs; ... }, using plain nixpkgs.lib
      # (genAttrs) so we don't need a flake-utils dependency.
      forAllSystems = f:
        nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        cbztool = pkgs.buildGoModule rec {
          pname = "cbztool";
          # Flake builds have no .git for Go to read the version from, so name
          # the commit; tagged releases are packaged (e.g. in nixpkgs) with the
          # tag's version instead.
          version = "0-unstable-${self.shortRev or self.dirtyShortRev or "unknown"}";

          src = ./.;

          # cbztool is stdlib-only (zero third-party dependencies), so there is
          # nothing to vendor. If a dependency is ever added, switch this to the
          # vendor hash Nix prints on the first failed build.
          vendorHash = null;

          # Build just the command; the binary is named after its directory.
          subPackages = [ "cmd/cbz" ];

          # Strip symbol table and DWARF, and stamp the version for --version
          # and the API User-Agent (internal/version).
          ldflags = [
            "-s"
            "-w"
            "-X codeberg.org/Nikode/cbztool/internal/version.version=${version}"
          ];

          meta = {
            description = "Fast, secure, zero-dependency CLI for .cbz comic archives";
            homepage = "https://codeberg.org/Nikode/cbztool";
            license = pkgs.lib.licenses.gpl3Plus;
            mainProgram = "cbz";
            platforms = pkgs.lib.platforms.unix;
          };
        };

        default = cbztool;
      });

      # `nix run .` / `nix run git+https://codeberg.org/Nikode/cbztool`
      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.system}.cbztool}/bin/cbz";
        };
      });

      # `nix develop` — a lightweight Go shell (devenv.nix remains the primary
      # dev environment; this is here so the flake is self-sufficient).
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.gofumpt ];
        };
      });

      # Lets other flakes do `overlays = [ cbztool.overlays.default ];` to get pkgs.cbztool.
      overlays.default = final: _prev: {
        cbztool = self.packages.${final.system}.cbztool;
      };

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
