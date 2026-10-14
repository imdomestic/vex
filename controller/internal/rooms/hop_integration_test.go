//go:build integration

package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func TestRealAssetDestinationDistinguishesAvailableAndRegisteredRoom(t *testing.T) {
	archive,metadataPath,manifestPath,scanner:=os.Getenv("CHORUS_IT_ASSET_ARCHIVE"),os.Getenv("CHORUS_IT_ASSET_METADATA"),os.Getenv("CHORUS_IT_BUILD_MANIFEST"),os.Getenv("CHORUS_IT_SCANNER")
	if archive==""||metadataPath==""||manifestPath==""||scanner==""{t.Skip("requires real published plaza metadata/archive, ready build manifest and scanner")}
	metadataBytes,err:=os.ReadFile(metadataPath);if err!=nil{t.Fatal(err)}
	var metadata model.AssetMetadata
	if err=json.Unmarshal(metadataBytes,&metadata);err!=nil{t.Fatal(err)}
	manifestBytes,err:=os.ReadFile(manifestPath);if err!=nil{t.Fatal(err)}
	var build model.Build
	if err=json.Unmarshal(manifestBytes,&build);err!=nil{t.Fatal(err)}
	if build.Status!="ready"||build.Minecraft==nil{t.Fatal("requires a real ready build")}
	root:=t.TempDir()
	db,err:=store.Open(filepath.Join(root,"state.db"));if err!=nil{t.Fatal(err)}
	defer db.Close()
	cfg:=config.Config{Server:config.Server{DataDir:filepath.Join(root,"data")},Minecraft:config.Minecraft{Version:*build.Minecraft},Assets:config.Assets{WorldScanner:scanner}}
	assetManager,err:=assets.New(cfg,db);if err!=nil{t.Fatal(err)}
	ctx:=context.Background()
	if _,_,err=assetManager.Publish(ctx,metadata,archive);err!=nil{t.Fatal(err)}
	asset,err:=assetManager.Get(ctx,metadata.ID);if err!=nil{t.Fatal(err)}
	manager:=&Manager{Cfg:cfg,DB:db,Assets:assetManager}
	available,err:=manager.assetDestination(ctx,asset,build)
	if err!=nil||available.RoomID!=nil||available.State!="available"||available.BuildID!=build.ID{t.Fatalf("unregistered asset destination: %+v %v",available,err)}
	id,err:=RoomID("asset",build.SHA,build.Loader,asset.ID);if err!=nil{t.Fatal(err)}
	if _,err=manager.Get(ctx,id);!errors.Is(err,store.ErrNotFound){t.Fatalf("listing created an asset room: %v",err)}
	room:=manager.newRoom(id,"asset",build.SHA,build.Loader,"asset:"+asset.ID)
	room.Asset=&asset.ID
	if err=db.Put(ctx,"room",id,room);err!=nil{t.Fatal(err)}
	registered,err:=manager.assetDestination(ctx,asset,build)
	if err!=nil||registered.RoomID==nil||*registered.RoomID!=id||registered.State!="stopped"{t.Fatalf("registered asset destination: %+v %v",registered,err)}
}
