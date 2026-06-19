{ pkgs, lib, config, inputs, ... }:

{
  # https://devenv.sh/packages/
  packages = [ pkgs.git pkgs.gopls pkgs.claude-code];

  # https://devenv.sh/languages/
  languages.go.enable = true;
}
