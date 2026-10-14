{ pkgs }:
let
  python = pkgs.python313.override {
    packageOverrides = self: previous: {
      nbt = self.buildPythonPackage {
        pname = "NBT";
        version = "1.5.1";
        format = "wheel";
        src = pkgs.fetchurl {
          url = "https://files.pythonhosted.org/packages/b9/ae/f3bdbbaccaca97a2bfdd87466a08717a2174f136e3b99a189d32d03d6526/NBT-1.5.1-py2.py3-none-any.whl";
          sha256 = "1503314d0374b983642f9f219890330ce19d9d85966a28bdb6bef1fb17c4c2c0";
        };
        pythonImportsCheck = [ "nbt" ];
      };
      anvil-parser2 = self.buildPythonPackage {
        pname = "anvil-parser2";
        version = "0.10.6";
        format = "wheel";
        src = pkgs.fetchurl {
          url = "https://files.pythonhosted.org/packages/80/15/23d22159c5699f5394ec1847adbf98c54aa9331ab2ce83748787d20ac5ef/anvil_parser2-0.10.6-py3-none-any.whl";
          sha256 = "72fc98c0b1f177fef4e60d3f42862c9c155d4609aa5a653d80335537dac99f4e";
        };
        dependencies = [ self.frozendict self.nbt ];
        pythonImportsCheck = [ "anvil" ];
      };
    };
  };
  runtime = python.withPackages (ps: [ ps.anvil-parser2 ps.nbtlib ]);
in pkgs.writeShellScriptBin "chorus-world-scanner" ''
  export PYTHONDONTWRITEBYTECODE=1
  exec ${runtime}/bin/python ${../tools/world-scanner/scan.py} "$@"
''
