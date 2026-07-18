{
  description = "JSI — Just Send It: dev shell (Go, Node, Wrangler)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go              # pinned in go.mod at M2.1; quic-go (S3) tracks latest 2 releases
            nodejs_22       # LTS for Wrangler v4
            wrangler        # Cloudflare Workers CLI v4
            golangci-lint
            gopls
          ];

          shellHook = ''
            echo "jsi dev shell: go $(go version | cut -d' ' -f3), node $(node --version), wrangler $(wrangler --version 2>/dev/null | tail -1)"
          '';
        };
      });
    };
}
