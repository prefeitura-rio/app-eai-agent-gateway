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
              # Go development
              go
              air # Go hot reload
              golangci-lint
              
              # Swagger documentation (will be installed via go install)
              # swag tool available via `go install github.com/swaggo/swag/cmd/swag@latest`
              
              # Docker
              docker-compose
              
              # Development tools
              just
              k6
              curl
              jq
              
              # Python (for scripts/analysis)
              (python313.withPackages (ps: with ps; [
                matplotlib
                numpy
                pandas
                requests
                seaborn
              ]))
              uv
            ];

            LD_LIBRARY_PATH = lib.makeLibraryPath [
              stdenv.cc.cc.lib
              zlib
            ];
          };
      }
    );
}