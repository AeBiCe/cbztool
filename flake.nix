{
  description = "cb — a fast, secure, zero-dependency CLI for .cbz comic archives";

  # Single input by design: cb is zero-dependency, and the flake keeps that
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
        cb = pkgs.buildGoModule {
          pname = "cb";
          version = "0.1.0";

          src = ./.;

          # cb is stdlib-only (zero third-party dependencies), so there is
          # nothing to vendor. If a dependency is ever added, switch this to the
          # vendor hash Nix prints on the first failed build.
          vendorHash = null;

          # Build just the root command (package main at the repo root).
          subPackages = [ "." ];

          # Smaller, reproducible binary; strip symbol table and DWARF.
          ldflags = [ "-s" "-w" ];

          meta = {
            description = "Fast, secure, zero-dependency CLI for .cbz comic archives";
            homepage = "https://codeberg.org/Nikode/cb";
            # TODO: must match the LICENSE file you add to the repo (see ROADMAP
            # "Packaging & distribution"). MIT is a placeholder.
            license = pkgs.lib.licenses.mit;
            mainProgram = "cb";
            platforms = pkgs.lib.platforms.unix;
          };
        };

        default = cb;
      });

      # `nix run .` / `nix run codeberg-flake#cb`
      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.system}.cb}/bin/cb";
        };
      });

      # `nix develop` — a lightweight Go shell (devenv.nix remains the primary
      # dev environment; this is here so the flake is self-sufficient).
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.gofumpt ];
        };
      });

      # Lets other flakes do `overlays = [ cb.overlays.default ];` to get pkgs.cb.
      overlays.default = final: _prev: {
        cb = self.packages.${final.system}.cb;
      };

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
