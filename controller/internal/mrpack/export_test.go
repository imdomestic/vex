package mrpack

import (
	"bytes"
	"testing"

	"github.com/Tnze/go-mc/nbt"
)

func TestServersNBTAndDownloadBoundaries(t *testing.T) {
	var output bytes.Buffer
	if err := writeServers(&output, "project / main-fabric", "main-fabric--project.example.test:25565"); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Servers []struct {
			Name string `nbt:"name"`
			IP   string `nbt:"ip"`
		} `nbt:"servers"`
	}
	if _, err := nbt.NewDecoder(bytes.NewReader(output.Bytes())).Decode(&decoded); err != nil || len(decoded.Servers) != 1 || decoded.Servers[0].Name != "project / main-fabric" || decoded.Servers[0].IP != "main-fabric--project.example.test:25565" {
		t.Fatal("servers.dat does not contain the actual room address", err)
	}
	for _, value := range []string{
		"http://cdn.modrinth.com/data/project/versions/version/mod.jar",
		"https://cdn.modrinth.com.attacker.test/data/project/versions/version/mod.jar",
		"https://cdn.modrinth.com:443/data/project/versions/version/mod.jar",
		"https://user@cdn.modrinth.com/data/project/versions/version/mod.jar",
		"https://cdn.modrinth.com/data/project/versions/version/mod.jar?token=private",
		"https://cdn.modrinth.com/data/project/versions/version/%2e%2e.jar",
		"https://cdn.modrinth.com/data/project/versions/version/%2fmod.jar",
		"https://127.0.0.1/mod.jar",
		"file:///etc/passwd",
	} {
		if trustedCDN(value) {
			t.Fatal("accepted an untrusted or signed download URL")
		}
	}
	if !trustedCDN("https://cdn.modrinth.com/data/P7dR8mSH/versions/abcdef12/fabric-api.jar") {
		t.Fatal("rejected an unsigned public CDN path")
	}
}
