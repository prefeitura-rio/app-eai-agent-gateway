{
  description = "Development environment";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      utils,
      nixpkgs,
      ...
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };
      in
      {
        devShells.default =
          with pkgs;
          mkShell {
            packages = [
              (python313.withPackages (ps: with ps; [
                matplotlib
                numpy
                pandas
                seaborn
              ]))
              uv
              k6
              just
            ];

            LD_LIBRARY_PATH = lib.makeLibraryPath [
              stdenv.cc.cc.lib
              zlib
            ];
          };
      }
    );
}