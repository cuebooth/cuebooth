{
  description = "CueBooth — live-event automation and control surface";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
    in
    {
      devShells = eachSystem (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              flutter
              gcc
            ] ++ pkgs.lib.optionals pkgs.stdenv.isLinux [
              ninja
              cmake
              clang
              pkg-config
              gtk3
            ];

            shellHook = ''
              echo "CueBooth dev shell"
              echo "  go:      $(go version)"
              echo "  flutter: $(flutter --version 2>&1 | head -1)"
            '';
          };
        }
      );
    };
}