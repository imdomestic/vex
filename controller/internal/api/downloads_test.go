package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/rooms"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func TestPackCanonicalRouteUsesAuthenticatedAssetStorage(t *testing.T) {
	root:=t.TempDir()
	db,err:=store.Open(filepath.Join(root,"state.db"));if err!=nil{t.Fatal(err)}
	defer db.Close()
	cfg:=config.Config{Server:config.Server{DataDir:filepath.Join(root,"data")}}
	manager,err:=assets.New(cfg,db);if err!=nil{t.Fatal(err)}
	key:=make([]byte,32);if _,err=rand.Read(key);err!=nil{t.Fatal(err)}
	token:=hex.EncodeToString(key)
	users,err:=toml.Marshal(struct{Users map[string]string `toml:"users"`}{Users:map[string]string{"integration":token}});if err!=nil{t.Fatal(err)}
	usersPath,keyPath:=filepath.Join(root,"users.toml"),filepath.Join(root,"signing.key")
	if err=os.WriteFile(usersPath,users,0600);err!=nil{t.Fatal(err)}
	if _,err=rand.Read(key);err!=nil{t.Fatal(err)}
	if err=os.WriteFile(keyPath,key,0600);err!=nil{t.Fatal(err)}
	identity,err:=auth.Load(usersPath,keyPath);if err!=nil{t.Fatal(err)}
	api:=&Server{Cfg:cfg,DB:db,Auth:identity,Rooms:&rooms.Manager{Assets:manager}}
	server:=httptest.NewServer(api.Handler());defer server.Close()
	path:="/v1/packs/plaza/"+strings.Repeat("a",64)+".zip"
	signed,err:=identity.SignURL(server.URL,path,time.Now().Add(time.Minute));if err!=nil{t.Fatal(err)}
	for _,test:=range []struct{url,token string;status int;code string}{
		{server.URL+path,"",401,"unauthorized"},
		{server.URL+path,token,404,"not_found"},
		{signed,"",404,"not_found"},
		{server.URL+"/v1/packs/plaza/invalid.zip",token,400,"invalid_request"},
		{server.URL+strings.TrimSuffix(path,".zip")+".jar",token,400,"invalid_request"},
	} {
		request,err:=http.NewRequest(http.MethodGet,test.url,nil);if err!=nil{t.Fatal(err)}
		if test.token!=""{request.Header.Set("Authorization","Bearer "+test.token)}
		response,err:=server.Client().Do(request);if err!=nil{t.Fatal(err)}
		var failure struct{Error model.Error `json:"error"`}
		err=json.NewDecoder(response.Body).Decode(&failure);response.Body.Close()
		if err!=nil||response.StatusCode!=test.status||failure.Error.Code!=test.code{t.Fatalf("pack route HTTP %d code=%s: %v",response.StatusCode,failure.Error.Code,err)}
	}
}
